package rob

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/indicator"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/sirupsen/logrus"
)

const ID = "rob"

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

	splSma  *indicator.SMA
	fptlEma *indicator.EWMA

	tl1Sma *indicator.SMA
	tl2Sma *indicator.SMA
	tl3Ema *indicator.EWMA

	ntzEma *indicator.EWMA
	trRma  *indicator.RMA

	prevClose float64
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

	s.splSma = &indicator.SMA{IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: 5}}
	s.fptlEma = &indicator.EWMA{IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: 18}}
	s.tl1Sma = &indicator.SMA{IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: 50}}
	s.tl2Sma = &indicator.SMA{IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: 89}}
	s.tl3Ema = &indicator.EWMA{IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: 144}}
	s.ntzEma = &indicator.EWMA{IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: 35}}
	s.trRma = &indicator.RMA{IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: 35}}

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
		s.splSma.PushK(k)
		s.fptlEma.PushK(k)
		s.tl1Sma.PushK(k)
		s.tl2Sma.PushK(k)
		s.tl3Ema.PushK(k)
		s.ntzEma.PushK(k)

		if s.prevClose != 0 {
			max := math.Max(math.Max(k.High.Float64()-k.Low.Float64(), math.Abs(k.High.Float64()-s.prevClose)), math.Abs(k.Low.Float64()-s.prevClose))
			s.trRma.Update(max)
		}

		s.prevClose = k.Close.Float64()
	}

	session.MarketDataStream.OnKLineClosed(func(k types.KLine) {
		if k.Symbol != s.Symbol {
			return
		}

		if k.Interval == s.Interval {
			log.Infof("start time: %s open: %v high: %v low: %v close: %v",
				k.StartTime.String(),
				k.Open,
				k.High,
				k.Low,
				k.Close,
			)

			s.splSma.PushK(k)
			s.fptlEma.PushK(k)
			s.tl1Sma.PushK(k)
			s.tl2Sma.PushK(k)
			s.tl3Ema.PushK(k)
			s.ntzEma.PushK(k)
			max := math.Max(math.Max(k.High.Float64()-k.Low.Float64(), math.Abs(k.High.Float64()-s.prevClose)), math.Abs(k.Low.Float64()-s.prevClose))
			s.trRma.Update(max)
			s.prevClose = k.Close.Float64()

			// 有持倉就不做
			if s.Position.GetQuantity().Float64() != 0 {
				return
			}

			long, short := rob(k)

			if long &&
				k.Close.Float64() > k.Open.Float64() &&
				k.Low.Float64() > s.splSma.Last() &&
				s.splSma.Last() > s.fptlEma.Last() &&
				s.fptlEma.Last() > s.tl1Sma.Last() &&
				s.fptlEma.Last() > s.tl2Sma.Last() &&
				s.fptlEma.Last() > s.tl3Ema.Last() &&
				s.fptlEma.Last() > s.ntzEma.Last() &&
				s.fptlEma.Last() > s.ntzEma.Last()+(s.trRma.Last()*0.5) {

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

			if short &&
				k.Open.Float64() > k.Close.Float64() &&
				k.High.Float64() < s.splSma.Last() &&
				s.splSma.Last() < s.fptlEma.Last() &&
				s.fptlEma.Last() < s.tl1Sma.Last() &&
				s.fptlEma.Last() < s.tl2Sma.Last() &&
				s.fptlEma.Last() < s.tl3Ema.Last() &&
				s.fptlEma.Last() < s.ntzEma.Last() &&
				s.fptlEma.Last() < s.ntzEma.Last()+(s.trRma.Last()*0.5) {
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
	})

	return nil
}

func rob(k types.KLine) (long bool, short bool) {
	z := 45.
	a := math.Abs(k.High.Float64() - k.Low.Float64())
	b := math.Abs(k.Close.Float64() - k.Open.Float64())
	c := z / 100
	rv := b < c*a

	x := k.Low.Float64() + (c * a)
	y := k.High.Float64() - (c * a)

	if rv && k.High.Float64() > y && k.Close.Float64() < y && k.Open.Float64() < y {
		return true, false
	}

	if rv && k.Low.Float64() < x && k.Close.Float64() > x && k.Open.Float64() > x {
		return false, true
	}

	return false, false
}
