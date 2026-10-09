package analysis_test

import (
	"math"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/analysis"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func series(scale, offset float64) model.Series {
	s := model.Series{Labels: map[string]string{"job": "go"}}
	for i := 0; i < 64; i++ {
		s.Points = append(s.Points, model.Point{Timestamp: 100000 + int64(i)*1000, Value: float64(i)*scale + offset})
	}
	return s
}
func TestShapeInvarianceRawScaleAndConstantWindows(t *testing.T) {
	a, err := analysis.Embed("orders", series(1, 0), 100000, 164000, "last", "shape")
	require.NoError(t, err)
	b, err := analysis.Embed("orders", series(20, 233), 100000, 164000, "last", "shape")
	require.NoError(t, err)
	assert.Equal(t, a.Values, b.Values)
	assert.NotEqual(t, a.ID, b.ID)
	assert.Equal(t, 1.0, a.Coverage)
	c, err := analysis.Embed("orders", series(1, 0), 100000, 164000, "last", "raw")
	require.NoError(t, err)
	assert.Equal(t, float32(63), c.Values[63])
	constant, err := analysis.Embed("orders", series(0, 233), 100000, 164000, "last", "shape")
	require.NoError(t, err)
	assert.Zero(t, constant.StdDev)
	for _, v := range constant.Values {
		assert.Zero(t, v)
	}
	a.CreatedAt = 233
	a.SetID()
	assert.Equal(t, a.ID, captureID(t, series(1, 0)))
}
func captureID(t *testing.T, s model.Series) string {
	p, err := analysis.Embed("orders", s, 100000, 164000, "last", "shape")
	require.NoError(t, err)
	return p.ID
}
func TestCoverageAndGapHandling(t *testing.T) {
	s := series(1, 0)
	s.Points = append(s.Points[:10], s.Points[14:]...)
	p, err := analysis.Embed("orders", s, 100000, 164000, "last", "raw")
	require.NoError(t, err)
	assert.Equal(t, 60.0/64, p.Coverage)
	assert.Equal(t, float32(12), p.Values[12])
	s = series(1, 0)
	s.Points = append(s.Points[:10], s.Points[20:]...)
	_, err = analysis.Embed("orders", s, 100000, 164000, "last", "shape")
	require.ErrorContains(t, err, "gap")
	s = series(1, 0)
	s.Points = s.Points[:40]
	_, err = analysis.Embed("orders", s, 100000, 164000, "last", "shape")
	require.ErrorContains(t, err, "48")
	_, err = analysis.Embed("orders", series(1, 0), 100000, 160000, "last", "shape")
	require.ErrorContains(t, err, "64 seconds")
	_, err = analysis.Embed("orders", series(1e25, 0), 100000, 164000, "last", "raw")
	require.Error(t, err)
	p, err = analysis.Embed("orders", series(1e305, 0), 100000, 164000, "last", "shape")
	require.NoError(t, err)
	assert.False(t, math.IsInf(p.StdDev, 0))
}
