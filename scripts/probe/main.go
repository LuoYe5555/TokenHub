// probe：本地只读诊断工具。
// 默认 zcode：dump billing/preview、balance、current（不发起领取）。
// -trae：dump 两个 Trae 账号的签到状态原始响应（不签到，只查状态）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"tokenhub/internal/config"
	"tokenhub/internal/provider/trae"
	"tokenhub/internal/provider/zcode"
	"tokenhub/internal/store"
)

func dumpJSON(label string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	s := string(b)
	if len(s) > 5000 {
		s = s[:5000] + " ...(截断)"
	}
	fmt.Printf("  -- %s --\n%s\n", label, s)
}

func main() {
	dataDir := flag.String("data", "data", "data 目录")
	traeMode := flag.Bool("trae", false, "诊断 Trae 签到状态")
	genDev := flag.Bool("gen-device", false, "claim 前给缺失 deviceId 的账号补一个（同 16 位数字格式）")
	flag.Parse()

	cfg, err := config.Load(*dataDir)
	if err != nil {
		fmt.Println("config:", err)
		return
	}
	st, err := store.Load(*dataDir)
	if err != nil {
		fmt.Println("store:", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if *traeMode {
		tp := trae.New(cfg)
		claimID := flag.Arg(0) // 可选：传账号 id 则实际执行一次签到 claim 并 dump 原始响应
		for _, a := range st.ByProvider("trae") {
			fmt.Printf("== Trae 账号 %s (id=%s uid=%s) ==\n", a.DisplayedName(), a.ID, a.UID)
			if claimID != "" && a.ID == claimID {
				if *genDev && a.ExtraGet(store.ExtraTraeDeviceID) == "" {
					n := int64(4400000000000000 + time.Now().UnixNano()%599999999999999)
					a.ExtraSet(store.ExtraTraeDeviceID, fmt.Sprintf("%d", n))
					_ = st.Upsert(a)
					fmt.Println("  已补 deviceId:", a.ExtraGet(store.ExtraTraeDeviceID))
				}
				raw, err := tp.DebugCheckinClaim(ctx, a)
				if err != nil {
					fmt.Println("  claim 失败:", err)
				} else {
					dumpJSON("checkin_claim", raw)
				}
				continue
			}
			raw := tp.RawCheckinSources(ctx, a)
			dumpJSON("checkin_status", raw["checkin_status"])
		}
		return
	}

	accs := st.ByProvider("zcode")
	if len(accs) == 0 {
		fmt.Println("没有 zcode 账号")
		return
	}
	p := zcode.New(cfg)
	for _, a := range accs {
		fmt.Printf("== 账号 %s (id=%s) ==\n", a.DisplayedName(), a.ID)
		plans, err := p.ClaimPlans(ctx, a)
		if err != nil {
			fmt.Println("ClaimPlans 失败:", err)
		}
		for _, pl := range plans {
			fmt.Printf("  可领取: id=%s name=%s grants=%v\n", pl.PlanID, pl.Name, pl.Grants)
		}
		raw := p.RawQuotaSources(ctx, a)
		for _, k := range []string{"billing_preview", "billing_balance", "billing_current", "bigmodel_quota_limit[0]", "bigmodel_quota_limit[1]", "bigmodel_subscription[0]", "bigmodel_subscription[1]"} {
			if v, ok := raw[k]; ok {
				dumpJSON(k, v)
			}
		}
	}
}
