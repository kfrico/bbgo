package adxstochrsi

import (
	"context"
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

	bbgo.SmartStops

	orderExecutor *bbgo.GeneralOrderExecutor

	bbgo.QuantityOrAmount // 單次購買數量或是金額

	Session *bbgo.ExchangeSession
	Market  types.Market

	Position    *types.Position    `persistence:"position"`
	ProfitStats *types.ProfitStats `persistence:"profit_stats"`

	Symbol   string         `json:"symbol"`
	Interval types.Interval `json:"interval"`

	UseAtrTakeProfit bool `json:"useAtrTakeProfit"`
	UseAtrStopLoss   bool `json:"useAtrStopLoss"`
	AtrWindow        int  `json:"atrWindow"`

	TakeProfit fixedpoint.Value `json:"takeProfit,omitempty"`
	StopLoss   fixedpoint.Value `json:"stopLoss,omitempty"`

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

func (s *Strategy) Initialize() error {
	return s.SmartStops.InitializeStopControllers(s.Symbol)
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

	s.SmartStops.Subscribe(session)
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

	s.SmartStops.RunStopControllers(ctx, session, s.orderExecutor.TradeCollector())

	s.ewma = &indicator.EWMA{IntervalWindow: types.IntervalWindow{Interval: s.EwmaInterval, Window: s.EwmaWindow}}

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

	// 綁定st，在初始化之後
	s.ewma.Bind(st)
	s.dmi.Bind(st)
	s.adx.Bind(st)
	s.stochrsi.Bind(st)

	longSignal := types.CrossOver(s.stochrsi.GetK(), s.stochrsi.GetD())
	shortSignal := types.CrossUnder(s.stochrsi.GetK(), s.stochrsi.GetD())

	session.MarketDataStream.OnKLineClosed(func(k types.KLine) {
		if k.Symbol != s.Symbol {
			return
		}

		if s.Position.GetQuantity().Float64() != 0 {
			isLongPosition := s.Position.IsLong()
			isShortPosition := s.Position.IsShort()

			if isLongPosition {

				if k.Close.Float64() > s.Position.AverageCost.Mul(fixedpoint.One.Add(s.TakeProfit)).Float64() {
					log.Infof("做多止盈 數量: %v 止盈價格: %v",
						s.Position.GetQuantity(),
						k.Close.Float64(),
					)
					log.Infof("%v", k)

					sellOrder := types.SubmitOrder{
						Symbol:   s.Symbol,
						Side:     types.SideTypeSell,
						Type:     types.OrderTypeMarket,
						Quantity: s.Position.GetQuantity(),
						Price:    k.Close,
						Market:   s.Market,
					}

					_, _ = s.orderExecutor.SubmitOrders(ctx, sellOrder)
				}

				if k.Close.Float64() < s.Position.AverageCost.Mul(fixedpoint.One.Sub(s.StopLoss)).Float64() {
					log.Infof("做多止損 數量: %v 止損價格: %v",
						s.Position.GetQuantity(),
						k.Close.Float64(),
					)
					log.Infof("%v", k)

					sellOrder := types.SubmitOrder{
						Symbol:   s.Symbol,
						Side:     types.SideTypeSell,
						Type:     types.OrderTypeMarket,
						Quantity: s.Position.GetQuantity(),
						Price:    k.Close,
						Market:   s.Market,
					}

					_, _ = s.orderExecutor.SubmitOrders(ctx, sellOrder)
				}
			}

			if isShortPosition {
				if k.Close.Float64() < s.Position.AverageCost.Mul(fixedpoint.One.Sub(s.TakeProfit)).Float64() {
					log.Infof("做空止盈 數量: %v 止盈價格: %v",
						s.Position.GetQuantity(),
						k.Close.Float64(),
					)
					log.Infof("%v", k)

					buyOrder := types.SubmitOrder{
						Symbol:   s.Symbol,
						Side:     types.SideTypeBuy,
						Type:     types.OrderTypeMarket,
						Quantity: s.Position.GetQuantity(),
						Price:    k.Close,
						Market:   s.Market,
					}

					_, _ = s.orderExecutor.SubmitOrders(ctx, buyOrder)
				}

				if k.Close.Float64() > s.Position.AverageCost.Mul(fixedpoint.One.Add(s.StopLoss)).Float64() {
					log.Infof("做空止損 數量: %v 止損價格: %v",
						s.Position.GetQuantity(),
						k.Close.Float64(),
					)
					log.Infof("%v", k)

					buyOrder := types.SubmitOrder{
						Symbol:   s.Symbol,
						Side:     types.SideTypeBuy,
						Type:     types.OrderTypeMarket,
						Quantity: s.Position.GetQuantity(),
						Price:    k.Close,
						Market:   s.Market,
					}

					_, _ = s.orderExecutor.SubmitOrders(ctx, buyOrder)
				}
			}

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
						buyTakeProfitPrice := k.Close.Mul(fixedpoint.One.Add(s.TakeProfit)) // 止盈價格
						buyStopLossPrice := k.Close.Mul(fixedpoint.One.Sub(s.StopLoss))
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

						log.Infof("做多 數量: %v 止盈價格: %v 止損價格: %v",
							buyQuantity,
							buyTakeProfitPrice,
							buyStopLossPrice,
						)
						log.Infof("%v", k)
					}
				}

				// 做空
				if s.ewma.Last() > k.Close.Float64() {
					srk := s.stochrsi.LastK()
					srd := s.stochrsi.LastD()

					if srk > 70 && srd > 75 && shortSignal.Last() {
						sellTakeProfitPrice := k.Close.Mul(fixedpoint.One.Sub(s.TakeProfit)) // 止盈價格
						sellStopLossPrice := k.Close.Mul(fixedpoint.One.Add(s.StopLoss))
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

						log.Infof("做空 數量: %v 止盈價格: %v 止損價格: %v",
							sellQuantity,
							sellTakeProfitPrice,
							sellStopLossPrice,
						)
						log.Infof("%v", k)
					}
				}
			}
		}

	})

	return nil
}
