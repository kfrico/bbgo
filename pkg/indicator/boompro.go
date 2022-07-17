package indicator

import (
	"math"

	"github.com/c9s/bbgo/pkg/types"
)

//go:generate callbackgen -type BOOMPRO
type BOOMPRO struct {
	types.SeriesBase
	types.Interval
	// Values types.Float64Slice
	K types.Float64Slice
	D types.Float64Slice

	CloseValues *types.Queue
	HPValues    *types.Queue
	FiltValues  *types.Queue
	PeakValues  *types.Queue

	HP2Values   *types.Queue
	Filt2Values *types.Queue
	Peak2Values *types.Queue

	SMA *SMA

	lpperiod float64
	k1       float64

	lpperiod2 float64
	k22       float64

	esize float64
	ey    float64
	pi    float64
	alpha float64

	updateCallbacks []func(k float64, d float64)
}

func NewBoomPro(lpperiod, k1, lpperiod2, k22 float64, trigno int, interval types.Interval) *BOOMPRO {
	b := &BOOMPRO{
		Interval:  interval,
		lpperiod:  lpperiod,
		k1:        k1,
		lpperiod2: lpperiod2,
		k22:       k22,
		esize:     60.,
		ey:        50.,
		pi:        2 * math.Asin(1),
		alpha:     (math.Cos(0.707*2*(2*math.Asin(1))/100) + math.Sin(0.707*2*(2*math.Asin(1))/100) - 1) / math.Cos(0.707*2*(2*math.Asin(1))/100),

		CloseValues: types.NewQueue(3),

		HPValues:   types.NewQueue(3),
		FiltValues: types.NewQueue(3),
		PeakValues: types.NewQueue(3),

		HP2Values:   types.NewQueue(3),
		Filt2Values: types.NewQueue(3),
		Peak2Values: types.NewQueue(3),

		SMA: &SMA{
			IntervalWindow: types.IntervalWindow{Window: trigno},
		},
	}

	return b
}

func (inc *BOOMPRO) Update(close float64) {
	HP := (1-inc.alpha/2)*(1-inc.alpha/2)*(close-2*inc.CloseValues.Index(0)+inc.CloseValues.Index(1)) + 2*(1-inc.alpha)*inc.HPValues.Index(0) - (1-inc.alpha)*(1-inc.alpha)*inc.HPValues.Index(1)

	//SuperSmoother Filter
	a1 := math.Exp(-1.414 * inc.pi / inc.lpperiod)
	b1 := 2 * a1 * math.Cos(1.414*inc.pi/inc.lpperiod)
	c2 := b1
	c3 := -a1 * a1
	c1 := 1 - c2 - c3
	Filt := c1*(HP+inc.HPValues.Index(0))/2 + c2*inc.FiltValues.Index(0) + c3*inc.FiltValues.Index(1)

	//Fast Attack - Slow Decay Algorithm
	Peak := 0.991 * inc.PeakValues.Index(0)

	if math.Abs(Filt) > Peak {
		Peak = math.Abs(Filt)
	}

	X := 0.
	if Peak != 0 {
		X = Filt / Peak
	}

	Quotient1 := (X + inc.k1) / (inc.k1*X + 1)
	q1 := Quotient1*inc.esize + inc.ey
	inc.SMA.Update(q1)
	inc.K.Push(inc.SMA.Last())

	// 更新資料
	inc.HPValues.Update(HP)
	inc.FiltValues.Update(Filt)
	inc.PeakValues.Update(Peak)
	// ===================================================================================

	HP2 := (1-inc.alpha/2)*(1-inc.alpha/2)*(close-2*inc.CloseValues.Index(0)+inc.CloseValues.Index(1)) + 2*(1-inc.alpha)*inc.HP2Values.Index(0) - (1-inc.alpha)*(1-inc.alpha)*inc.HP2Values.Index(1)

	//SuperSmoother Filter
	a12 := math.Exp(-1.414 * inc.pi / inc.lpperiod2)
	b12 := 2 * a12 * math.Cos(1.414*inc.pi/inc.lpperiod2)
	c22 := b12
	c32 := -a12 * a12
	c12 := 1 - c22 - c32

	Filt2 := c12*(HP2+inc.HP2Values.Index(0))/2 + c22*inc.Filt2Values.Index(0) + c32*inc.Filt2Values.Index(1)

	//Fast Attack - Slow Decay Algorithm
	Peak2 := 0.991 * inc.Peak2Values.Index(0)

	if math.Abs(Filt2) > Peak2 {
		Peak2 = math.Abs(Filt2)
	}

	X2 := 0.
	//Normalized Roofing Filter
	if Peak2 != 0 {
		X2 = Filt2 / Peak2
	}

	Quotient4 := (X2 + inc.k22) / (inc.k22*X2 + 1)
	q4 := Quotient4*inc.esize + inc.ey

	inc.D.Push(q4)

	// 更新資料
	inc.HP2Values.Update(HP2)
	inc.Filt2Values.Update(Filt2)
	inc.Peak2Values.Update(Peak2)

	// 最後更新收盤價
	inc.CloseValues.Update(close)
}

func (inc *BOOMPRO) LastK() float64 {
	if len(inc.K) == 0 {
		return 0.0
	}
	return inc.K[len(inc.K)-1]
}

func (inc *BOOMPRO) LastD() float64 {
	if len(inc.K) == 0 {
		return 0.0
	}
	return inc.D[len(inc.D)-1]
}

func (inc *BOOMPRO) PushK(k types.KLine) {
	inc.Update(k.Close.Float64())
}

var _ types.SeriesExtend = &BOOMPRO{}

func (inc *BOOMPRO) CalculateAndUpdate(allKLines []types.KLine) {
	if inc.CloseValues == nil {
		for _, k := range allKLines {
			inc.PushK(k)
		}
	} else {
		k := allKLines[len(allKLines)-1]
		inc.PushK(k)
	}

	inc.EmitUpdate(inc.LastK(), inc.LastD())
}

func (inc *BOOMPRO) handleKLineWindowUpdate(interval types.Interval, window types.KLineWindow) {
	if inc.Interval != interval {
		return
	}

	inc.CalculateAndUpdate(window)
}

func (inc *BOOMPRO) Bind(updater KLineWindowUpdater) {
	updater.OnKLineWindowUpdate(inc.handleKLineWindowUpdate)
}

func (inc *BOOMPRO) GetD() types.Series {
	return &inc.D
}

func (inc *BOOMPRO) GetK() types.Series {
	return &inc.K
}
