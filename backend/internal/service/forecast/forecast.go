// Package forecast 预期利润/营收（同花顺 iFinD 一致预期，FY1/2/3）
// 数据源：raw/ifind.Client.Forecast → basic_data_service + ths_fore_*_stock + indiparams:[asOf]
// 复用分层红线：route 禁止触 DB，全部走 service 方法；货币/单位在 service 层统一
package forecast

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"stockanalyzer/internal/raw/ifind"
	"stockanalyzer/internal/service/managerlog"
	"stockanalyzer/internal/service/marketcode"
)

var ErrNotSupported = errors.New("forecast source not supported")

type ForecastSource interface {
	Name() string
	Forecast(ctx context.Context, codes []string, asOf string) (map[string]*ifind.ForecastData, error)
}

// IFIndForecast iFinD 预测 Provider（basic_data_service）
type IFIndForecast struct{ Raw *ifind.Client }

func (p *IFIndForecast) Name() string { return "ifind" }

func (p *IFIndForecast) Forecast(ctx context.Context, codes []string, asOf string) (map[string]*ifind.ForecastData, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	m, err := p.Raw.Forecast(ctx, codes, asOf)
	if err != nil && ifind.IsNotSupported(err) {
		return nil, ErrNotSupported
	}
	return m, err
}

// ForecastResult 单码结构化预期（字符串透传 + 数值解析）
type ForecastResult struct {
	Code      string  `json:"code"`
	FullCode  string  `json:"full_code"`
	Name      string  `json:"name,omitempty"`
	AsOf      string  `json:"as_of"`
	EpsFY1    *string  `json:"eps_fy1,omitempty"`
	NpFY1     *string  `json:"np_fy1,omitempty"`
	NpFY2     *string  `json:"np_fy2,omitempty"`
	NpFY3     *string  `json:"np_fy3,omitempty"`
	MbiFY1    *string  `json:"mbi_fy1,omitempty"`
	MbiFY2    *string  `json:"mbi_fy2,omitempty"`
	MbiFY3    *string  `json:"mbi_fy3,omitempty"`
	EpsFY1Num *float64 `json:"eps_fy1_num,omitempty"`
	NpFY1Num  *float64 `json:"np_fy1_num,omitempty"`
	NpFY2Num  *float64 `json:"np_fy2_num,omitempty"`
	NpFY3Num  *float64 `json:"np_fy3_num,omitempty"`
	MbiFY1Num *float64 `json:"mbi_fy1_num,omitempty"`
	MbiFY2Num *float64 `json:"mbi_fy2_num,omitempty"`
	MbiFY3Num *float64 `json:"mbi_fy3_num,omitempty"`
	Source    string   `json:"source"`
}

type Manager struct {
	Raw     *ifind.Client
	sources []ForecastSource
	Codes   *marketcode.Registry
}

func New(sources ...ForecastSource) *Manager { return &Manager{sources: sources} }

// Forecast 批量预期（按 80/批切分，并发每批独立降级，最终合并）
func (m *Manager) Forecast(ctx context.Context, codes []string, asOf string) (map[string]*ForecastResult, []string, error) {
	if len(codes) == 0 {
		return map[string]*ForecastResult{}, nil, nil
	}
	codes = normalizeCodes(codes)
	chunks := ifind.ChunkCodes(codes, 80)
	type chunkRes struct {
		m   map[string]*ifind.ForecastData
		src string
		err error
	}
	out := make([]chunkRes, len(chunks))
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, cks []string) {
			defer wg.Done()
			mm, src, err := m.forecastOneChunk(ctx, cks, asOf)
			out[idx] = chunkRes{m: mm, src: src, err: err}
		}(i, chunk)
	}
	wg.Wait()
	merged := map[string]*ForecastResult{}
	var errs []error
	var tried []string
	for _, r := range out {
		if r.err != nil {
			if errors.Is(r.err, ErrNotSupported) {
				continue
			}
			errs = append(errs, r.err)
			continue
		}
		if r.src != "" {
			tried = append(tried, r.src)
		}
		for thscode, fd := range r.m {
			full := thscode
			bare := marketcode.Bare(full)
			res := &ForecastResult{
				Code:     bare,
				FullCode: full,
				AsOf:     fd.AsOf,
				Source:   r.src,
			}
			if m.Codes != nil {
				res.Name = m.Codes.Name(full)
			}
			if fd.EpsFY1 != "" {
				s := strings.TrimSpace(fd.EpsFY1)
				res.EpsFY1 = &s
				res.EpsFY1Num = parseNum(s)
			}
			if fd.NpFY1 != "" {
				s := strings.TrimSpace(fd.NpFY1)
				res.NpFY1 = &s
				res.NpFY1Num = parseNum(s)
			}
			if fd.NpFY2 != "" {
				s := strings.TrimSpace(fd.NpFY2)
				res.NpFY2 = &s
				res.NpFY2Num = parseNum(s)
			}
			if fd.NpFY3 != "" {
				s := strings.TrimSpace(fd.NpFY3)
				res.NpFY3 = &s
				res.NpFY3Num = parseNum(s)
			}
			if fd.MbiFY1 != "" {
				s := strings.TrimSpace(fd.MbiFY1)
				res.MbiFY1 = &s
				res.MbiFY1Num = parseNum(s)
			}
			if fd.MbiFY2 != "" {
				s := strings.TrimSpace(fd.MbiFY2)
				res.MbiFY2 = &s
				res.MbiFY2Num = parseNum(s)
			}
			if fd.MbiFY3 != "" {
				s := strings.TrimSpace(fd.MbiFY3)
				res.MbiFY3 = &s
				res.MbiFY3Num = parseNum(s)
			}
			merged[bare] = res
			merged[full] = res
		}
	}
	if len(merged) == 0 {
		if len(errs) > 0 {
			label := managerlog.JoinNames(tried)
			if label == "" {
				label = "forecast"
			}
			log.Printf("[预期] %s 失败: %v", label, errs)
			return nil, tried, errors.Join(errs...)
		}
		return map[string]*ForecastResult{}, tried, ErrNotSupported
	}
	if len(errs) > 0 {
		log.Printf("[预期] 部分批次失败（已合并 %d 码）: %v", len(merged)/2, errs)
	}
	return merged, tried, nil
}

func (m *Manager) forecastOneChunk(ctx context.Context, codes []string, asOf string) (map[string]*ifind.ForecastData, string, error) {
	var errs []error
	var tried []string
	for _, s := range m.sources {
		tried = append(tried, s.Name())
		mm, err := s.Forecast(ctx, codes, asOf)
		if err == nil && len(mm) > 0 {
			label := managerlog.JoinNames(tried)
			log.Printf("[预期] %s 命中 %s %d码 asOf=%s", label, s.Name(), len(mm), asOf)
			return mm, s.Name(), nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, "", errors.Join(errs...)
	}
	return nil, "", ErrNotSupported
}

func normalizeCodes(codes []string) []string {
	out := make([]string, 0, len(codes))
	seen := map[string]bool{}
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		upper := strings.ToUpper(c)
		if seen[upper] {
			continue
		}
		seen[upper] = true
		out = append(out, upper)
	}
	return out
}

func parseNum(s string) *float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	if s == "" || s == "--" || s == "-" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
