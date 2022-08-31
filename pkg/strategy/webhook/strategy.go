package webhook

import (
	"context"
	"errors"
	"fmt"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
	"github.com/sirupsen/logrus"

	"github.com/labstack/echo/v4"
)

const ID = "webhook"

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
}

func (s *Strategy) ID() string {
	return ID
}

func (s *Strategy) InstanceID() string {
	return fmt.Sprintf("%s:%s", ID, s.Symbol)
}

func (s *Strategy) Subscribe(session *bbgo.ExchangeSession) {
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

func (s *Strategy) CloseLongPosition(ctx context.Context, price fixedpoint.Value) {
	if s.Position.GetQuantity().Float64() != 0 && s.Position.IsLong() {
		log.Infof("做多關閉倉位 數量: %v 價格: %v",
			s.Position.GetQuantity(),
			price,
		)

		sellOrder := types.SubmitOrder{
			Symbol:   s.Symbol,
			Side:     types.SideTypeSell,
			Type:     types.OrderTypeMarket,
			Quantity: s.Position.GetQuantity(),
			Price:    price,
			Market:   s.Market,
		}

		_, _ = s.orderExecutor.SubmitOrders(ctx, sellOrder)
	}
}

func (s *Strategy) CloseShortPosition(ctx context.Context, price fixedpoint.Value) {
	if s.Position.GetQuantity().Float64() != 0 && s.Position.IsShort() {
		log.Infof("做空關閉倉位 數量: %v 價格: %v",
			s.Position.GetQuantity(),
			price,
		)

		buyOrder := types.SubmitOrder{
			Symbol:   s.Symbol,
			Side:     types.SideTypeBuy,
			Type:     types.OrderTypeMarket,
			Quantity: s.Position.GetQuantity(),
			Price:    price,
			Market:   s.Market,
		}

		_, _ = s.orderExecutor.SubmitOrders(ctx, buyOrder)
	}
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

	go func() {
		e := echo.New()

		e.POST("/order/:coin", s.webhook)

		e.Logger.Fatal(e.Start(":8168"))
	}()

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
		}
	})

	return nil
}
