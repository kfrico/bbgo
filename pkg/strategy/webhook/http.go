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

	if s.Symbol != coin {
		return c.JSON(http.StatusOK, "ok")
	}

	if n.Symbol == "" {
		n.Symbol = coin
	}

	log.Info(n)

	ms := strings.Split(n.Message, "|")

	if len(ms) != 5 {
		return c.JSON(http.StatusOK, "ok")
	}

	log.Infof("webhook message %v", ms)

	price, _ := s.Session.LastPrice(s.Symbol)

	switch ms[3] {
	case "方向: Long":
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

		_, err = s.orderExecutor.SubmitOrders(context.Background(), buyOrder)

		if err != nil {
			log.Errorf("做多 數量: %v 價格: %v Error: %v",
				buyQuantity,
				price,
				err,
			)
		}

		log.Infof("做多 數量: %v 價格: %v",
			buyQuantity,
			price,
		)
	case "方向: LongStop":
		s.CloseLongPosition(context.Background(), price)
	case "方向: Short":
		s.CloseLongPosition(context.Background(), price)

		if s.EnableShort {
			sellQuantity := s.QuantityOrAmount.CalculateQuantity(price)

			sellOrder := types.SubmitOrder{
				Symbol:   s.Symbol,
				Side:     types.SideTypeSell,
				Type:     types.OrderTypeMarket,
				Quantity: sellQuantity,
				Price:    price,
				Market:   s.Market,
			}

			_, err = s.orderExecutor.SubmitOrders(context.Background(), sellOrder)

			if err != nil {
				log.Errorf("做空 數量: %v 價格: %v Error: %v",
					sellQuantity,
					price,
					err,
				)
			}

			log.Infof("做空 數量: %v 價格: %v",
				sellQuantity,
				price,
			)
		}
	case "方向: ShortStop":
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
