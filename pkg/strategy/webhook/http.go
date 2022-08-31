package webhook

import (
	"context"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/c9s/bbgo/pkg/types"

	"github.com/labstack/echo/v4"
)

func (s *Strategy) webhook(c echo.Context) (err error) {
	n := new(Notice)

	if err = c.Bind(n); err != nil {
		defer c.Request().Body.Close()
		body, _ := ioutil.ReadAll(c.Request().Body)

		n.Message = string(body)
	}

	coin := c.Param("coin")

	if n.Symbol == "" {
		n.Symbol = coin
	}

	log.Info(n)

	ms := strings.Split(n.Message, "|")

	if len(ms) != 4 {
		return c.JSON(http.StatusOK, "ok")
	}

	log.Infof("webhook message %v", ms)

	if s.Symbol != ms[1] {
		return c.JSON(http.StatusOK, "ok")
	}

	price, _ := s.Session.LastPrice(s.Symbol)

	switch ms[2] {
	case "Long":
		s.CloseShortPosition(context.Background(), price)

		buyQuantity := s.QuantityOrAmount.CalculateQuantity(price)

		buyOrder := types.SubmitOrder{
			Symbol:   s.Symbol,
			Side:     types.SideTypeBuy,
			Type:     types.OrderTypeMarket,
			Quantity: buyQuantity,
			Price:    price,
			Market:   s.Market,
		}

		_, _ = s.orderExecutor.SubmitOrders(context.Background(), buyOrder)

		log.Infof("做多 數量: %v 價格: %v",
			buyQuantity,
			price,
		)
	case "LongStop":
		s.CloseLongPosition(context.Background(), price)
	case "Short":
		s.CloseLongPosition(context.Background(), price)

		sellQuantity := s.QuantityOrAmount.CalculateQuantity(price)

		sellOrder := types.SubmitOrder{
			Symbol:   s.Symbol,
			Side:     types.SideTypeSell,
			Type:     types.OrderTypeMarket,
			Quantity: sellQuantity,
			Price:    price,
			Market:   s.Market,
		}

		_, _ = s.orderExecutor.SubmitOrders(context.Background(), sellOrder)

		log.Infof("做空 數量: %v 價格: %v",
			sellQuantity,
			price,
		)
	case "ShortStop":
		s.CloseShortPosition(context.Background(), price)
	}

	return c.JSON(http.StatusOK, "ok")
}

type Notice struct {
	Symbol  string  `json:"symbol" form:"symbol" query:"symbol"`
	Price   float64 `json:"price" form:"price" query:"price"`
	Volume  float64 `json:"volume" form:"volume" query:"volume"`
	Message string  `json:"message" form:"message" query:"message"`
}
