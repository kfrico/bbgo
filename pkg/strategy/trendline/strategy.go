package trendline

import (
	"context"
	"errors"
	"fmt"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	// "github.com/c9s/bbgo/pkg/indicator"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/sirupsen/logrus"
	"gonum.org/v1/gonum/floats"
)

const ID = "trendline"

const limit = 100

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

	BreakWindow int `json:"breakWindow,omitempty"`
	// EwmaInterval types.Interval `json:"ewmaInterval"`
	// EwmaWindow   int            `json:"ewmaWindow"`

	// DmiWindow int `json:"dmiWindow"`
	// AdxWindow int `json:"adxWindow"`

	// StochrsiKWindow     int `json:"stochrsiKWindow"`
	// StochrsiDWindow     int `json:"stochrsiDWindow"`
	// StochrsiRsiWindow   int `json:"stochrsiRsiWindow"`
	// StochrsiStochWindow int `json:"stochrsiStochWindow"`

	// ewma     *indicator.EWMA
	// dmi      *indicator.DMI
	// stochrsi *indicator.STOCHRSI
	// adx      *indicator.ADX
	//
	highValue   types.Float64Slice
	lowValue    types.Float64Slice
	volumeValue types.Float64Slice
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
	// session.Subscribe(types.KLineChannel, s.Symbol, types.SubscribeOptions{Interval: s.EwmaInterval})

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

	// s.ewma = &indicator.EWMA{IntervalWindow: types.IntervalWindow{Interval: s.EwmaInterval, Window: s.EwmaWindow}}

	// s.dmi = &indicator.DMI{
	// IntervalWindow: types.IntervalWindow{Interval: s.Interval, Window: s.DmiWindow},
	// ADXSmoothing:   14,
	// }

	// s.adx = indicator.NewAdx(types.IntervalWindow{Interval: s.Interval, Window: s.AdxWindow})

	// s.stochrsi = indicator.NewStochrsi(s.StochrsiKWindow, s.StochrsiDWindow, s.StochrsiRsiWindow, s.StochrsiStochWindow, s.Interval)

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
		s.highValue.Push(k.High.Float64())
		s.lowValue.Push(k.Low.Float64())
		s.volumeValue.Push(k.Volume.Float64())
	}

	// Ewmaklines, ok := st.KLinesOfInterval(s.EwmaInterval)

	// 初始化ewma數據 指標 (時間不一樣)
	// for _, k := range *Ewmaklines {
	// 	s.ewma.PushK(k)
	// }

	// // 綁定st，在初始化之後
	// s.ewma.Bind(st)
	// s.dmi.Bind(st)
	// s.adx.Bind(st)
	// s.stochrsi.Bind(st)

	// longSignal := types.CrossOver(s.stochrsi.GetK(), s.stochrsi.GetD())
	// shortSignal := types.CrossUnder(s.stochrsi.GetK(), s.stochrsi.GetD())

	session.MarketDataStream.OnKLineClosed(func(k types.KLine) {
		if k.Symbol != s.Symbol {
			return
		}

		if k.Interval == s.Interval {
			if s.Position.GetQuantity().Float64() == 0 {
				maxVolue := s.volumeValue.Mean()

				price := cla(s.highValue, s.BreakWindow)

				buySign := false

				// todo 增加量能判斷
				if price != 0 && k.Close.Float64() > price && k.Volume.Float64() > maxVolue {
					buySign = true
					fmt.Println("long price:", price)
					fmt.Println("k:", k)
				}

				shortPrice := claShort(s.lowValue, s.BreakWindow)

				sellSign := false

				if shortPrice != 0 && k.Close.Float64() < shortPrice && k.Volume.Float64() > maxVolue {
					sellSign = true
					fmt.Println("short price:", shortPrice)
					fmt.Println("k:", k)
				}

				// 做多
				if buySign {
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

				// 	// 做空
				if sellSign {
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

			// 最後再把K線資料更新
			s.highValue.Push(k.High.Float64())

			// 最多紀錄幾筆
			if len(s.highValue) > limit {
				s.highValue = s.highValue[len(s.highValue)-limit:]
			}

			s.lowValue.Push(k.Low.Float64())

			if len(s.lowValue) > limit {
				s.lowValue = s.lowValue[len(s.lowValue)-limit:]
			}

			s.volumeValue.Push(k.Volume.Float64())

			if len(s.volumeValue) > limit {
				s.volumeValue = s.volumeValue[len(s.volumeValue)-limit:]
			}
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
	})

	return nil
}

func cla(vals types.Float64Slice, breakWindow int) float64 {
	limit := len(vals)

	maxIndex := floats.MaxIdx(vals)

	if len(vals[maxIndex+1:]) == 0 {
		return 0
	}

	maxIndex2 := floats.MaxIdx(vals[maxIndex+1:])

	if len(vals[maxIndex+1+maxIndex2:]) <= breakWindow {
		return 0
	}

	slopeVal := slope(float64(maxIndex), vals[maxIndex], float64(maxIndex+1+maxIndex2), vals[maxIndex+1+maxIndex2])

	// expectedPrice := vals[maxIndex+1+maxIndex2] + float64(limit-maxIndex+1+maxIndex2)*slopeVal
	// +1就是不包含自己[index+1:]
	for k, v := range vals[maxIndex+1+maxIndex2+1:] {
		if v > vals[maxIndex+1+maxIndex2]+float64(k+1)*slopeVal {
			if len(vals[maxIndex+1+maxIndex2+1+k:]) <= breakWindow+10 {
				return 0
			}

			return cla(vals[maxIndex+1+maxIndex2+1+k:], breakWindow)
		}
	}
	// fmt.Println("Long:", vals[maxIndex], vals[maxIndex+1+maxIndex2])

	return vals[maxIndex+1+maxIndex2] + float64(limit-maxIndex+1+maxIndex2)*slopeVal
}

func claShort(vals types.Float64Slice, breakWindow int) float64 {
	limit := len(vals)

	minIndex := floats.MinIdx(vals)

	if len(vals[minIndex+1:]) == 0 {
		return 0
	}

	minIndex2 := floats.MinIdx(vals[minIndex+1:])

	if len(vals[minIndex+1+minIndex2:]) <= breakWindow {
		return 0
	}

	slopeVal := slope(float64(minIndex), vals[minIndex], float64(minIndex+1+minIndex2), vals[minIndex+1+minIndex2])
	// expectedPrice := vals[minIndex+1+minIndex2] + float64(limit-minIndex+1+minIndex2)*slopeVal
	// +1就是不包含自己[index+1:]
	for k, v := range vals[minIndex+1+minIndex2+1:] {
		if v < vals[minIndex+1+minIndex2]+float64(k+1)*slopeVal {
			if len(vals[minIndex+1+minIndex2+1+k:]) <= breakWindow+10 {
				return 0
			}

			return claShort(vals[minIndex+1+minIndex2+1+k:], breakWindow)
		}
	}
	// fmt.Println("Short:", vals[minIndex], vals[minIndex+1+minIndex2])

	return vals[minIndex+1+minIndex2] + float64(limit-minIndex+1+minIndex2)*slopeVal
}

func slope(x1, y1, x2, y2 float64) float64 {
	if x2-x1 != 0 {
		return (y2 - y1) / (x2 - x1)
	}

	return 0
}

// // driver code to check the above function
// var x1 = 4;
// var y1 = 2;
// var x2 = 2;
// var y2 = 5;
// console.log("Slope is " + slope(x1, y1, x2, y2));
