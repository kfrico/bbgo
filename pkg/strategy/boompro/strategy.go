package boompro

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

const ID = "boompro"

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

	VolatilityLength int `json:"volatilityLength"`

	boompro    *indicator.BOOMPRO
	spikeQueue *types.Queue
	hull       *indicator.HULL
	atr        *indicator.ATR
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) Initialize() error {
	return s.SmartStops.InitializeStopControllers(s.Symbol)
}

func (s *Strategy) InstanceID() string {
	return fmt.Sprintf("%s:%s", ID, s.Symbol)
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: s.Interval})
	session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: types.Interval4h})

	s.SmartStops.Subscribe(session)
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

	s.SmartStops.RunStopControllers(ctx, session, s.orderExecutor.TradeCollector())

	// Initialize a custom indicator
	s.atr = &indicator.ATR{
		IntervalWindow: types.IntervalWindow{
			Interval: s.Interval,
			Window:   s.AtrWindow,
		},
	}

	st, ok := session.MarketDataStore(s.Symbol)

	if !ok {
		return nil
	}

	klines, ok := st.KLinesOfInterval(s.Interval)

	if !ok {
		return nil
	}

	s.boompro = indicator.NewBoomPro(6, 0, 27, 0.3, 2, s.Interval)
	s.spikeQueue = types.NewQueue(s.VolatilityLength)
	s.hull = &indicator.HULL{IntervalWindow: types.IntervalWindow{Window: 400, Interval: types.Interval4h}}

	// 初始化數據 指標
	for _, k := range *klines {
		s.atr.Update(k.High.Float64(), k.Low.Float64(), k.Close.Float64())
		s.spikeQueue.Update(k.Close.Float64() - k.Open.Float64())
		s.boompro.Update(k.Close.Float64())
		s.hull.Update(k.Close.Float64())
	}

	// 綁定st，在初始化之後
	s.atr.Bind(st)
	s.boompro.Bind(st)

	var sellTakeProfitPrice fixedpoint.Value // 做空停利價
	var sellStopLossPrice fixedpoint.Value   // 做空停損價
	var buyTakeProfitPrice fixedpoint.Value  // 做多停利價
	var buyStopLossPrice fixedpoint.Value    // 做多停損價

	longSignal := types.CrossOver(s.boompro.GetK(), s.boompro.GetD())
	shortSignal := types.CrossUnder(s.boompro.GetK(), s.boompro.GetD())

	session.MarketDataStream.OnKLineClosed(func(k types.KLine) {
		if k.Symbol != s.Symbol {
			return
		}

		if s.Position.GetQuantity().Float64() != 0 {
			isLongPosition := s.Position.IsLong()
			isShortPosition := s.Position.IsShort()

			if isLongPosition {
				if k.Close.Float64() > buyTakeProfitPrice.Float64() {
					log.Infof("做多止盈 數量: %v 止盈價格: %v",
						s.Position.GetQuantity(),
						buyTakeProfitPrice,
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

				if k.Close.Float64() < buyStopLossPrice.Float64() {
					log.Infof("做多止損 數量: %v 止損價格: %v",
						s.Position.GetQuantity(),
						buyStopLossPrice,
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
				if k.Close.Float64() < sellTakeProfitPrice.Float64() {
					log.Infof("做空止盈 數量: %v 止盈價格: %v",
						s.Position.GetQuantity(),
						sellTakeProfitPrice,
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

				if k.Close.Float64() > sellStopLossPrice.Float64() {
					log.Infof("做空止損 數量: %v 止損價格: %v",
						s.Position.GetQuantity(),
						sellStopLossPrice,
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
			s.hull.Update(k.Close.Float64())

			spike := k.Close.Float64() - k.Open.Float64()
			s.spikeQueue.Update(spike)

			x := s.spikeQueue.Stdev(s.VolatilityLength)
			y := x * -1

			log.Infof("start time: %s open: %v high: %v low: %v close: %v spike: %f x: %f y: %f boompro k: %f boompro d: %f H[0]: %f H[2]: %f",
				k.StartTime.String(),
				k.Open,
				k.High,
				k.Low,
				k.Close,
				spike,
				x,
				y,
				s.boompro.LastK(),
				s.boompro.LastD(),
				s.hull.Index(0),
				s.hull.Index(2),
			)

			// 作多
			if s.hull.Index(0) > s.hull.Index(2) &&
				k.Close.Float64() > s.hull.Index(0) &&
				k.Close.Float64() > s.hull.Index(2) &&
				longSignal.Last() {
				if spike > x {
					buyTakeProfitPrice = k.Close.Mul(fixedpoint.One.Add(s.TakeProfit)) // 止盈價格
					buyStopLossPrice = k.Close.Mul(fixedpoint.One.Sub(s.StopLoss))

					if s.UseAtrTakeProfit {
						buyTakeProfitPrice = k.Close.Add(fixedpoint.NewFromFloat(s.atr.Last() * 1.5)) // 止盈價格
					}

					if s.UseAtrStopLoss {
						buyStopLossPrice = k.Close.Sub(fixedpoint.NewFromFloat(s.atr.Last() * 1.5))
					}

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

			// 作空
			if s.hull.Index(0) < s.hull.Index(2) &&
				k.Close.Float64() < s.hull.Index(0) &&
				k.Close.Float64() < s.hull.Index(2) &&
				shortSignal.Last() {
				if spike < y {
					sellTakeProfitPrice = k.Close.Mul(fixedpoint.One.Sub(s.TakeProfit)) // 止盈價格
					sellStopLossPrice = k.Close.Mul(fixedpoint.One.Add(s.StopLoss))

					if s.UseAtrTakeProfit {
						sellTakeProfitPrice = k.Close.Sub(fixedpoint.NewFromFloat(s.atr.Last() * 1.5)) // 止盈價格
					}

					if s.UseAtrStopLoss {
						sellStopLossPrice = k.Close.Add(fixedpoint.NewFromFloat(s.atr.Last() * 1.5))
					}

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

	})

	return nil
}
