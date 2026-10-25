// Package autoslide 用 Windows SendInput 在操作系统层模拟真人滑块拖拽。
// SendInput 产生的事件与真实鼠标硬件事件一致（trusted），配合真实 WebView2
// 窗口，对阿里云风控来说与真人手滑无异。
// 轨迹刻意做成"人味"：慢起-中段加速-收尾减速、随机微停顿、Y 轴抖动、
// 概率性冲过头再回拉修正，绝不平滑匀速。
// 所有坐标均为「物理屏幕像素」（虚拟桌面坐标系）。
package autoslide

import (
	"errors"
	"math"
	"math/rand"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32     = syscall.NewLazyDLL("user32.dll")
	procSendIn = user32.NewProc("SendInput")
	procMetric = user32.NewProc("GetSystemMetrics")
	procFocus  = user32.NewProc("SetForegroundWindow")
	procIconic = user32.NewProc("IsIconic")
	procShow   = user32.NewProc("ShowWindow")
)

const (
	smXVirtual  = 76
	smYVirtual  = 77
	smCXVirtual = 78
	smCYVirtual = 79

	inputMouse = 0

	mfMove     = 0x0001
	mfLeftDown = 0x0002
	mfLeftUp   = 0x0004
	mfVirtual  = 0x4000
	mfAbsolute = 0x8000

	swRestore = 9
)

type mouseINPUT struct {
	DX, DY    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type input struct {
	Type uint32
	_    uint32 // x64 联合体 8 字节对齐的填充
	Mi   mouseINPUT
}

func sendMouse(flags uint32, dx, dy int32) error {
	var in input
	in.Type = inputMouse
	in.Mi = mouseINPUT{DX: dx, DY: dy, Flags: flags}
	r, _, err := procSendIn.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if r != 1 {
		if err != nil && err.Error() != "The operation completed successfully." {
			return errors.New("SendInput 失败: " + err.Error())
		}
	}
	return nil
}

func metrics(code int) int {
	r, _, _ := procMetric.Call(uintptr(code))
	return int(int32(r))
}

// toAbs 物理像素 → SendInput 绝对坐标（0~65535，虚拟桌面）。
func toAbs(x, y int) (int32, int32) {
	vx, vy := metrics(smXVirtual), metrics(smYVirtual)
	cx, cy := metrics(smCXVirtual), metrics(smCYVirtual)
	if cx <= 1 || cy <= 1 {
		return 0, 0
	}
	nx := int(math.Round(float64(x-vx) * 65535.0 / float64(cx-1)))
	ny := int(math.Round(float64(y-vy) * 65535.0 / float64(cy-1)))
	if nx < 0 {
		nx = 0
	}
	if nx > 65535 {
		nx = 65535
	}
	if ny < 0 {
		ny = 0
	}
	if ny > 65535 {
		ny = 65535
	}
	return int32(nx), int32(ny)
}

func moveTo(x, y int) error {
	dx, dy := toAbs(x, y)
	return sendMouse(mfMove|mfAbsolute|mfVirtual, dx, dy)
}

// FocusWindow 把目标窗口带到前台（最小化则先还原）。
func FocusWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if r, _, _ := procIconic.Call(hwnd); r != 0 {
		_, _, _ = procShow.Call(hwnd, swRestore)
		time.Sleep(120 * time.Millisecond)
	}
	_, _, _ = procFocus.Call(hwnd)
	time.Sleep(120 * time.Millisecond)
}

// Drag 从 (x1,y1) 按下拖到 (x2,y2) 再松开，轨迹拟人。
// 返回拖拽是否完成（总耗时约 0.8~2.2 秒）。
func Drag(x1, y1, x2, y2 int) error {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	if err := moveTo(x1, y1); err != nil {
		return err
	}
	time.Sleep(time.Duration(150+rnd.Intn(200)) * time.Millisecond) // 握住前的停顿

	if err := sendMouse(mfLeftDown, 0, 0); err != nil {
		return err
	}
	time.Sleep(time.Duration(70+rnd.Intn(120)) * time.Millisecond) // 按下稳定期

	// 步数与总时长随机：人手拖一个滑块 0.6~1.4s
	steps := 26 + rnd.Intn(24)
	total := time.Duration(600+rnd.Intn(800)) * time.Millisecond
	_ = total // 总时长由下方逐帧休眠累计体现，保留随机量便于后续按帧均分调优

	// 冲过头概率 35%，过头量 4~14px
	overshoot := 0
	if rnd.Float64() < 0.35 {
		overshoot = 4 + rnd.Intn(10)
		if x2 >= x1 {
			x2 += overshoot
		} else {
			x2 -= overshoot
		}
	}

	// 预生成 1~2 个微停顿步（人手卡顿/犹豫）
	pauses := map[int]bool{}
	for i := 0; i < 1+rnd.Intn(2); i++ {
		pauses[steps/4+rnd.Intn(steps/2)] = true
	}

	dxTotal := float64(x2 - x1)
	dyTotal := float64(y2-y1) + float64(2-rnd.Intn(5)) // 目标 y 略偏
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		// easeInOutQuad 变体：慢起-加速-减速
		var ease float64
		if t < 0.5 {
			ease = 2 * t * t
		} else {
			ease = 1 - math.Pow(-2*t+2, 2)/2
		}
		x := float64(x1) + dxTotal*ease
		y := float64(y1) + dyTotal*ease
		// Y 轴抖动 ±2px，X 轴 ±1px（人手不稳）
		jx := rnd.Float64()*2 - 1
		jy := rnd.Float64()*4 - 2
		if err := moveTo(int(math.Round(x+jx)), int(math.Round(y+jy))); err != nil {
			sendMouse(mfLeftUp, 0, 0)
			return err
		}
		if pauses[i] {
			time.Sleep(time.Duration(25+rnd.Intn(60)) * time.Millisecond) // 卡顿
		}
		// 步间隔抖动：8~26ms
		time.Sleep(time.Duration(8+rnd.Intn(18)) * time.Millisecond)
	}

	if overshoot != 0 {
		// 回拉修正到真实目标
		x2r := x2 - overshoot
		if x2 >= x1 {
			x2r = x2 - overshoot
		}
		back := 3 + rnd.Intn(4)
		for i := 1; i <= back; i++ {
			t := float64(i) / float64(back)
			x := int(math.Round(float64(x2) + (float64(x2r)-float64(x2))*t))
			y := int(math.Round(float64(y2) + (rnd.Float64()*2 - 1)))
			_ = moveTo(x, y)
			time.Sleep(time.Duration(14+rnd.Intn(20)) * time.Millisecond)
		}
	}

	time.Sleep(time.Duration(90+rnd.Intn(160)) * time.Millisecond) // 松开前的确认停顿
	if err := sendMouse(mfLeftUp, 0, 0); err != nil {
		return err
	}
	return nil
}
