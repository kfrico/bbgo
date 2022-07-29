package adxstochrsi

import (
	"context"
	"errors"
	"fmt"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/indicator"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/sirupsen/logrus"
)

const ID = "adxstochrsi"

var log = logrus.WithField("strategy", ID)

func init() {
	bbgo.RegisterStrategy(ID, &Strategy{})
}

type Strategy struct {
	*bbgo.Notifiability
	*bbgo.Environment

	orderExecutor *bbgo.GeneralOrderExecutor
	ExitMethods   bbgo.ExitMethodSet `json:"exits"`

	bbgo.QuantityOrAmount // 單次購買數量或是金額

	Session              *bbgo.ExchangeSession
	StandardIndicatorSet *bbgo.StandardIndicatorSet
	Market               types.Market

	Position    *types.Position    `persistence:"position"`
	ProfitStats *types.ProfitStats `persistence:"profit_stats"`

	Symbol   string         `json:"symbol"`
	Interval types.Interval `json:"interval"`

	UseAtrTakeProfit bool `json:"useAtrTakeProfit"`
	UseAtrStopLoss   bool `json:"useAtrStopLoss"`
	AtrWindow        int  `json:"atrWindow"`

	EwmaInterval types.Interval `json:"ewmaInterval"`
	EwmaWindow   int            `json:"ewmaWindow"`

	DmiWindow int `json:"dmiWindow"`
	AdxWindow int `json:"adxWindow"`

	StochrsiKWindow     int `json:"stochrsiKWindow"`
	StochrsiDWindow     int `json:"stochrsiDWindow"`
	StochrsiRsiWindow   int `json:"stochrsiRsiWindow"`
	StochrsiStochWindow int `json:"stochrsiStochWindow"`

	ewma     *indicator.EWMA
	dmi      *indicator.DMI
	stochrsi *indicator.STOCHRSI
	adx      *indicator.ADX
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) InstanceID() string {
	return fmt.Sprintf("%s:%s", ID, s.Symbol)
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: types.Interval1m})
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: s.Interval})
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: s.EwmaInterval})

	s.ExitMethods.SetAndSubscribe(session, s)
}

func (s *Strategy) Validate() error {
	if len(s.Symbol) == 0 {
		return errors.New("symbol is required")
	}

	return nil
}

func (s *Strategy) CurrentPosition() *types.Position {
	return s.Position
}

func (s *Strategy) ClosePosition(ctx context.Context, percentage fixedpoint.Value) error {
	return s.orderExecutor.ClosePosition(ctx, percentage)
}

func (s *Strategy) Run(ctx context.Context, orderExecutor bbgo.OrderExecutor, session *bbgo.ExchangeSession) error {
	log.Infof("%+v", s)

	if s.Position == nil {
		s.Position = types.NewPositionFromMarket(s.Market)
	}

	if session.MakerFeeRate.Sign() > 0 || session.TakerFeeRate.Sign() > 0 {
		s.Position.SetExchangeFeeRate(session.ExchangeName, types.ExchangeFee{
			MakerFeeRate: session.MakerFeeRate,
			TakerFeeRate: session.TakerFeeRate,
		})
	}

	if s.ProfitStats == nil {
		s.ProfitStats = types.NewProfitStats(s.Market)
	}

	// Always update the position fields
	s.Position.Strategy = ID
	s.Position.StrategyInstanceID = s.InstanceID()

	s.orderExecutor = bbgo.NewGeneralOrderExecutor(session, s.Symbol, ID, s.InstanceID(), s.Position)
	s.orderExecutor.BindEnvironment(s.Environment)
	s.orderExecutor.BindProfitStats(s.ProfitStats)
	s.orderExecutor.Bind()

	s.orderExecutor.TradeCollector().OnPositionUpdate(func(position *types.Position) {
		bbgo.Sync(s)
	})

	s.ExitMethods.Bind(session, s.orderExecutor)

	s.dmi = &indicator.DMI{
		IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: s.DmiWindow},
		ADXSmoothing:   14,
	}

	s.adx = indicator.NewAdx(types.IntervalWindow{Interval: s.Interval, Window: s.AdxWindow})

	s.stochrsi = indicator.NewStochrsi(s.StochrsiKWindow, s.StochrsiDWindow, s.StochrsiRsiWindow, s.StochrsiStochWindow, s.Interval)

	st, ok := session.MarketDataStore(s.Symbol)

	if !ok {
		return nil
	}

	klines, ok := st.KLinesOfInterval(s.Interval)

	if !ok {
		return nil
	}

	// 綁定st，在初始化之後
	s.ewma = s.StandardIndicatorSet.EWMA(types.IntervalWindow{Interval: s.EwmaInterval, Window: s.EwmaWindow})
	// s.ewma.Bind(st)
	s.dmi.Bind(st)
	s.adx.Bind(st)
	s.stochrsi.Bind(st)

	// 初始化數據 指標
	for _, k := range *klines {
		s.dmi.PushK(k)
		s.adx.PushK(k)
		s.stochrsi.PushK(k)
	}

	Ewmaklines, ok := st.KLinesOfInterval(s.EwmaInterval)

	// 初始化ewma數據 指標 (時間不一樣)
	for _, k := range *Ewmaklines {
		s.ewma.PushK(k)
	}

	longSignal := types.CrossOver(s.stochrsi.GetK(), s.stochrsi.GetD())
	shortSignal := types.CrossUnder(s.stochrsi.GetK(), s.stochrsi.GetD())

	session.MarketDataStream.OnKLineClosed(func(k types.KLine) {
		if k.Symbol != s.Symbol {
			return
		}

		if s.Position.GetQuantity().Float64() != 0 {
			return
		}

		if k.Interval == s.Interval {
			log.Infof("start time: %s open: %v high: %v low: %v close: %v adx: %f ewma: %f stochrsi k: %f stochrsi d: %f",
				k.StartTime.String(),
				k.Open,
				k.High,
				k.Low,
				k.Close,
				s.adx.Last(),
				s.ewma.Last(),
				s.stochrsi.LastK(),
				s.stochrsi.LastD(),
			)

			if s.adx.Last() > 50 || s.adx.Index(1) > 50 || s.adx.Index(2) > 50 {
				// 做多
				if k.Close.Float64() > s.ewma.Last() {
					srk := s.stochrsi.LastK()
					srd := s.stochrsi.LastD()

					if srk < 30 && srd < 25 && longSignal.Last() {
						buyQuantity := s.QuantityOrAmount.CalculateQuantity(k.Close)

						buyOrder := types.SubmitOrder{
							Symbol:   s.Symbol,
							Side:     types.SideTypeBuy,
							Type:     types.OrderTypeMarket,
							Quantity: buyQuantity,
							Price:    k.Close,
							Market:   s.Market,
						}

						_, _ = s.orderExecutor.SubmitOrders(ctx, buyOrder)

						log.Infof("做多 數量: %v",
							buyQuantity,
						)
						log.Infof("%v", k)
					}
				}

				// 做空
				if s.ewma.Last() > k.Close.Float64() {
					srk := s.stochrsi.LastK()
					srd := s.stochrsi.LastD()

					if srk > 70 && srd > 75 && shortSignal.Last() {
						sellQuantity := s.QuantityOrAmount.CalculateQuantity(k.Close)

						sellOrder := types.SubmitOrder{
							Symbol:   s.Symbol,
							Side:     types.SideTypeSell,
							Type:     types.OrderTypeMarket,
							Quantity: sellQuantity,
							Price:    k.Close,
							Market:   s.Market,
						}

						_, _ = s.orderExecutor.SubmitOrders(ctx, sellOrder)

						log.Infof("做空 數量: %v",
							sellQuantity,
						)
						log.Infof("%v", k)
					}
				}
			}
		}

	})

	return nil
}
