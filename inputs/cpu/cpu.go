// Package cpu 是 Categraf 的 CPU 采集器。
// 作用：定期读取本机 CPU 使用情况（用户态、系统态、空闲等），
// 计算成百分比后放入 SampleList，供后续转成时序数据发往夜莺。
package cpu

import (
	"log"

	cpuUtil "github.com/shirou/gopsutil/v3/cpu"

	"flashcat.cloud/categraf/config"
	"flashcat.cloud/categraf/inputs"
	"flashcat.cloud/categraf/inputs/system"
	"flashcat.cloud/categraf/types"
)

// ========== 常量 ==========
// const 定义常量，程序运行期间不会改变。
// 这里把采集器的"名字"固定为 "cpu"，后面注册、配置里都会用这个名字。
//
// 实际例子：程序里到处会用 inputName，值就是字符串 "cpu"。
const inputName = "cpu"

// ========== 结构体（采集器本体）==========
// type 结构体名 struct { ... } 用来定义一种"数据类型"。
// CPUStats 就是 CPU 采集器：里面既有配置，也有运行时用的状态。
type CPUStats struct {
	// ps：系统信息接口，用来真正去读 CPU 时间（跨平台：Linux/Windows/Mac）。
	ps system.PS

	// lastStats：上一次采集的 CPU 时间快照。
	// map[string]cpuUtil.TimesStat 表示：key 是字符串（如 "cpu0"），value 是 CPU 时间统计。
	// CPU 使用率 = (当前时间 - 上次时间) / 时间间隔，所以必须保留"上次"的数据。
	//
	// 实际例子（假设单核、不按核采集时可能只有一个 "cpu-total"）：
	//   lastStats = map[string]cpuUtil.TimesStat{
	//       "cpu-total": { User: 100.1, System: 50.2, Idle: 800.5, ... },  // 上次采集的各状态累计秒数
	//   }
	lastStats map[string]cpuUtil.TimesStat

	// 嵌入 config.PluginConfig：相当于把"插件通用配置"（如 interval 等）直接放进 CPUStats，
	// 这样 CPUStats 就自动拥有 PluginConfig 里的所有字段，不用再写一遍。
	config.PluginConfig

	// CollectPerCPU：是否按每个 CPU 核心分别采集。
	// 反引号里的 `toml:"collect_per_cpu"` 叫 struct tag，表示从 TOML 配置文件里
	// 读到的键名是 "collect_per_cpu"，会填到这个字段。
	//
	// 实际例子：conf/input.cpu/cpu.toml 里写 collect_per_cpu = true，
	// 那这里就是 true，times 里会有多条（cpu0, cpu1, ...）；写 false 则只有一条总体（cpu-total）。
	CollectPerCPU bool `toml:"collect_per_cpu"`
}

// ========== 注册：程序启动时自动执行 ==========
// init 是 Go 的保留函数名：不能手动调用，程序启动时、在 main 之前自动执行。
// 作用：把 CPU 采集器"登记"到 Categraf 的采集器表里，这样 Agent 才知道有 cpu 这个采集器。
//
// 实际例子：程序一启动，就会执行一次 init()，相当于在「采集器花名册」里写上一行：
//   "cpu" → 用这个函数来创建 CPU 采集器（此时还没真正创建，只是登记了「怎么创建」）。
// 等 Agent 读到 conf/input.cpu/ 下有配置时，才会调用这个函数，真正 new 一个 CPUStats。
func init() {
	// inputs.Add(名字, 创建函数)
	// 第二个参数是「匿名函数」：func() inputs.Input { ... }
	// 含义：无参数，返回一个 inputs.Input 类型的值。
	// 这里不立刻创建 CPUStats，而是把"创建方法"存起来，等配置里启用了 cpu 再真正创建（按需创建）。
	inputs.Add(inputName, func() inputs.Input {
		// return &CPUStats{...} 返回一个「指向 CPUStats 的指针」。
		// & 表示取地址；不用 & 会返回"值本身"，大结构体一般用指针，避免复制。
		return &CPUStats{
			ps: system.NewSystemPS(), // 获取当前系统的 PS 实现（读 CPU 等）
		}
	})
}

// ========== 实现 inputs.Input 接口：Clone ==========
// (c *CPUStats) 是「方法接收者」：表示这个方法属于 *CPUStats，调用时用 c.xxx。
// Clone 要求返回一个"新的、独立的"采集器实例，用于多实例场景（例如多份配置）。
//
// 实际例子：Agent 加载配置时可能根据配置「克隆」出多份采集器；
// 每份有自己的 lastStats，互不干扰。CPU 采集器通常只有一份，所以这里只是返回一个新对象。
func (c *CPUStats) Clone() inputs.Input {
	return &CPUStats{
		ps: system.NewSystemPS(),
	}
}

// ========== 实现 inputs.Input 接口：Name ==========
// 返回采集器名字，和 const inputName 一致，用于日志、配置关联等。
//
// 实际例子：Name() 返回 "cpu"，日志里会看到 "input: local.cpu started"。
func (c *CPUStats) Name() string {
	return inputName
}

// ========== 核心：采集逻辑（Gather）==========
// Gather 是采集器最重要的方法：读系统数据，算出指标，放进 slist。
// slist 是 *types.SampleList，即「指向 SampleList 的指针」，里面会装很多 Sample（指标点）。
func (c *CPUStats) Gather(slist *types.SampleList) {
	// 调用系统接口获取 CPU 时间；c.CollectPerCPU 表示是否按核采集，true 表示包含所有核。
	// Go 常见写法：返回值, err := 某函数()，然后下面 if err != nil 判断错误。
	times, err := c.ps.CPUTimes(c.CollectPerCPU, true)
	if err != nil {
		log.Println("E! failed to get cpu metrics:", err)
		return // 出错就提前返回，不继续往下执行
	}

	// 实际例子：times 是「当前这一刻」系统返回的 CPU 时间累计值（单位秒），例如：
	//   times = []cpuUtil.TimesStat{
	//       { CPU: "cpu-total", User: 120.5, System: 55.3, Idle: 850.2, Iowait: 10.1, ... },
	//   }
	// 若 CollectPerCPU=true 可能有多条：{ CPU: "cpu0", ... }, { CPU: "cpu1", ... }。

	// for range 遍历切片：times 是 []cpuUtil.TimesStat，每次循环取一个元素。
	// _ 表示忽略索引（下标），只用到 cts（当前这一项的 CPU 时间统计）。
	for _, cts := range times {
		// tags 是「标签」，后面会挂在每个指标上，用于区分不同系列（例如 cpu=cpu0, cpu1）。
		// map[string]string 表示：键、值都是字符串。
		tags := map[string]string{
			"cpu": cts.CPU,
		}
		// 实际例子：tags = map[string]string{ "cpu": "cpu-total" } 或 "cpu0", "cpu1" 等。

		// 当前这一次的「总 CPU 时间」和「活跃时间」（非 idle）
		total := totalCPUTime(cts)
		active := activeCPUTime(cts)
		// 实际例子：total=1036.1（秒），active=185.9（秒）。

		// 第一次采集时 lastStats 为空，没有「上一次」数据，算不出使用率，直接跳过。
		if len(c.lastStats) == 0 {
			continue // continue 表示跳过本次循环，进入下一次
		}

		// 从 lastStats 里取出「上一次」这一核的数据。
		// Go 的 map 取值会返回两个值：value 和 ok（是否存在）。
		lastCts, ok := c.lastStats[cts.CPU]
		if !ok {
			continue // 没有上一轮数据就跳过
		}
		// 实际例子：lastCts = { User: 100.0, System: 50.0, Idle: 800.0, ... }（15 秒前的快照）

		lastTotal := totalCPUTime(lastCts)
		lastActive := activeCPUTime(lastCts)
		totalDelta := total - lastTotal // 时间差，作为分母
		// 实际例子：lastTotal=950.0, total=1036.1 → totalDelta=86.1（秒），即过去约 15 秒的 CPU 总增量。

		if totalDelta < 0 {
			log.Println("W! current total CPU time is less than previous total CPU time")
			break // 异常情况，直接跳出循环
		}

		if totalDelta == 0 {
			continue // 避免除零
		}

		// fields：多个指标名 -> 数值。map[string]interface{} 表示值可以是任意类型（这里实际都是 float64）。
		// 使用率 = (当前 - 上次) / 时间差 * 100，得到百分比。
		fields := map[string]interface{}{
			"user":       100 * (cts.User - lastCts.User - (cts.Guest - lastCts.Guest)) / totalDelta,
			"system":     100 * (cts.System - lastCts.System) / totalDelta,
			"idle":       100 * (cts.Idle - lastCts.Idle) / totalDelta,
			"nice":       100 * (cts.Nice - lastCts.Nice - (cts.GuestNice - lastCts.GuestNice)) / totalDelta,
			"iowait":     100 * (cts.Iowait - lastCts.Iowait) / totalDelta,
			"irq":        100 * (cts.Irq - lastCts.Irq) / totalDelta,
			"softirq":    100 * (cts.Softirq - lastCts.Softirq) / totalDelta,
			"steal":      100 * (cts.Steal - lastCts.Steal) / totalDelta,
			"guest":      100 * (cts.Guest - lastCts.Guest) / totalDelta,
			"guest_nice": 100 * (cts.GuestNice - lastCts.GuestNice) / totalDelta,
			"active":     100 * (active - lastActive) / totalDelta,
		}
		// 实际例子（假设这 15 秒内 user 增 20、idle 增 60、totalDelta=86）：
		//   fields = map[string]interface{}{
		//       "user": 23.2, "system": 6.2, "idle": 69.7, "active": 30.3, ...
		//   }
		// 这些就是「过去 15 秒内的 CPU 使用率百分比」。

		// 把这一组指标写入 SampleList。
		// "cpu_usage" 是前缀，会和 fields 里的 key 组成最终指标名，如 cpu_usage_user、cpu_usage_system；
		// tags 会挂在每个指标上。
		slist.PushSamples("cpu_usage", fields, tags)
		// 实际例子：slist 里会多出多条 Sample，例如：
		//   { Metric: "cpu_usage_user",   Value: 23.2, Labels: { "cpu": "cpu-total" } }
		//   { Metric: "cpu_usage_system", Value:  6.2, Labels: { "cpu": "cpu-total" } }
		//   { Metric: "cpu_usage_idle",   Value: 69.7, Labels: { "cpu": "cpu-total" } }
		//   { Metric: "cpu_usage_active", Value: 30.3, Labels: { "cpu": "cpu-total" } }
		//   ... 共 11 条（对应 fields 里 11 个 key）
	}

	// 更新 lastStats：把本次的 times 存起来，下次采集时当「上一次」用。
	c.lastStats = make(map[string]cpuUtil.TimesStat) // make 用来创建 map，并初始化
	for _, cts := range times {
		c.lastStats[cts.CPU] = cts
	}
	// 实际例子：下次 Gather 被调用时，c.lastStats["cpu-total"] 就是这次保存的 cts，
	// 就可以用「这次 - 上次」再除以时间差算出使用率。
}

// totalCPUTime 计算某次 CPU 时间统计的「总时间」（所有状态相加）。
// 参数 t 是值传递；返回 float64，即总秒数。
//
// 实际例子：t = { User: 120, System: 50, Idle: 800, ... } → total = 120+50+...+800 = 1036.1。
func totalCPUTime(t cpuUtil.TimesStat) float64 {
	total := t.User + t.System + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal + t.Idle
	return total
}

// activeCPUTime 计算「活跃时间」= 总时间 - 空闲时间，用来算 CPU 使用率。
//
// 实际例子：total=1036.1, Idle=800 → active=236.1；使用率 = (active增量)/(total增量)*100。
func activeCPUTime(t cpuUtil.TimesStat) float64 {
	active := totalCPUTime(t) - t.Idle
	return active
}
