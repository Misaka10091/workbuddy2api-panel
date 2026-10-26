// humanize.go 周期任务的人味化抖动：余额刷新不再按精确固定间隔打卡。
//
// 背景（2026-10-24 风控复盘）：多个账号在同一出口 IP 上以「每 N 分钟整点一次余额
// 刷新」的完全一致节拍活动，是最好认的机器人形状。小时级任务已有账号摊开
// （spread.go），分钟级周期此前仍是死板的 ticker——本文件给它们补上同一类随机性。
//
// 设计口径：偏移是随机的，但**对单个进程实例稳定**——同一次运行里每轮的抖动幅度
// 取自同一个种子（进程启动时间），重启后换新值。不做逐轮独立随机（那会让测试不可
// 复现，也没有额外收益：上游看到的是「间隔在抖」而不是「间隔恒定但相位在抖」），
// 也不做全局常量（多实例部署共享配置时会又抖到一起）。
package scheduler

import (
	"hash/fnv"
	"sync/atomic"
	"time"
)

// jitterSeed 进程启动时刻（Unix 纳秒），jitterFrac 的稳定随机源。测试可用
// setJitterSeed 固定，让抖动可复现。
var jitterSeed atomic.Int64

func init() { jitterSeed.Store(time.Now().UnixNano()) }

// setJitterSeed 固定抖动种子（测试专用；0 = 恢复为当前时刻）。
func setJitterSeed(n int64) {
	if n == 0 {
		n = time.Now().UnixNano()
	}
	jitterSeed.Store(n)
}

// jitterFrac 稳定的 [0,1) 伪随机分数：key 相同 → 结果相同；key 不同 → 均匀散开。
// 与 jitterOffset 同一思路（FNV 而非 math/rand），只是散列输入换成调用方给的 key。
func jitterFrac(key string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte{0})
	var b [8]byte
	n := jitterSeed.Load()
	b[0] = byte(n)
	b[1] = byte(n >> 8)
	b[2] = byte(n >> 16)
	b[3] = byte(n >> 24)
	b[4] = byte(n >> 32)
	b[5] = byte(n >> 40)
	b[6] = byte(n >> 48)
	b[7] = byte(n >> 56)
	_, _ = h.Write(b[:])
	return float64(h.Sum64()>>11) / float64(1<<53)
}

// periodicJitter 周期 interval 的下一次触发抖动量：±frac*interval 内的稳定偏移。
// frac<=0（未启用）返回 0——旧行为逐字保留。
func periodicJitter(interval time.Duration, frac float64, key string) time.Duration {
	if frac <= 0 || interval <= 0 {
		return 0
	}
	return time.Duration((jitterFrac(key) - 0.5) * 2 * float64(interval) * frac)
}

// spreadIntervalStagger 余额刷新遍历的账号级起跑错峰窗口（frac*interval 的量级）。
// <=0 返回 0（全并发）。
func spreadIntervalStagger(interval time.Duration, frac float64) time.Duration {
	if frac <= 0 || interval <= 0 {
		return 0
	}
	return time.Duration(float64(interval) * frac)
}

// spreadStaggerDelay 单账号在余额刷新摊开窗口内的偏移。与账号摊开（spread.go）的
// 区别：那边是「排程任务当天确定性」，这边每个周期重算——余额刷新一天跑几百轮，
// 固定映射会让「每轮第 i 个账号」永远落在同一相对位置。
func spreadStaggerDelay(window time.Duration, uid string) time.Duration {
	if window <= 0 {
		return 0
	}
	return time.Duration(jitterFrac("bal|"+uid) * float64(window))
}
