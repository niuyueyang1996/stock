package main

import (
	"fmt"
	"stockanalyzer/internal/db"
	"stockanalyzer/internal/service/valuation"
)

func main() {
	g, _ := db.Open("/tmp/apidiff_home/data/etf.db")
	defer func() { sql, _ := g.DB(); _ = sql.Close() }()
	live := valuation.NewLive(g, nil)
	price := 83.03
	out := live.ComputeLive("000333", &price, "", nil)
	fmt.Printf("pe=%v pb=%v ttm=%v mv=%v\n", out["pe"], out["pb"], out["ttm_net_profit"], out["total_mv"])
}
