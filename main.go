package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/go-viper/mapstructure/v2"
	"github.com/gonutz/w32/v2"
	"github.com/google/uuid"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/ui/wm"
	"github.com/rodrigocfd/windigo/win"
	"github.com/rodrigocfd/windigo/win/co"
	"github.com/shahfarhadreza/go-gdiplus"
	draw2 "golang.org/x/image/draw"
	"golang.org/x/sys/windows"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// --------------------------
// 資料結構與全域變數
// --------------------------

type AuthorizeArgs struct {
	ClientId string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

type Authorize struct {
	Nonce         string `json:"nonce"`
	Cmd           string `json:"cmd"`
	AuthorizeArgs `json:"args"`
}

var apiURL = "https://api.overlayed.dev"
var clientID = "905987126099836938"
var selfID = ""

type ChannelStates struct {
	Id          string      `json:"id" mapstructure:"id"`
	Name        string      `json:"name" mapstructure:"name"`
	Type        int         `json:"type" mapstructure:"type"`
	Bitrate     int         `json:"bitrate" mapstructure:"bitrate"`
	UserLimit   int         `json:"user_limit" mapstructure:"user_limit"`
	GuildId     string      `json:"guild_id" mapstructure:"guild_id"`
	Position    int         `json:"position" mapstructure:"position"`
	VoiceStates []UserState `json:"voice_states" mapstructure:"voice_states"`
}
type User struct {
	Id            string `json:"id" mapstructure:"id"`
	Username      string `json:"username" mapstructure:"username"`
	Discriminator string `json:"discriminator" mapstructure:"discriminator"`
	Avatar        string `json:"avatar" mapstructure:"avatar"`
	Bot           bool   `json:"bot" mapstructure:"bot"`
}
type VoiceState struct {
	Mute     bool `json:"mute" mapstructure:"mute"`
	Deaf     bool `json:"deaf" mapstructure:"deaf"`
	SelfMute bool `json:"self_mute" mapstructure:"self_mute"`
	SelfDeaf bool `json:"self_deaf" mapstructure:"self_deaf"`
	Suppress bool `json:"suppress" mapstructure:"suppress"`
	Talking  bool `json:"-" mapstructure:"-"`
}
type UserState struct {
	VoiceState VoiceState `json:"voice_state" mapstructure:"voice_state"`
	User       User       `json:"user" mapstructure:"user"`
	Nick       string     `json:"nick" mapstructure:"nick"`
	Volume     int        `json:"volume" mapstructure:"volume"`
	Mute       bool       `json:"mute" mapstructure:"mute"`
}

var subs = []string{
	"SPEAKING_START",
	"SPEAKING_STOP",
	"VOICE_STATE_CREATE",
	"VOICE_STATE_DELETE",
	"VOICE_STATE_UPDATE",
}
var currentChannel = ""
var userMap = map[string]*UserState{}
var userMapLock sync.Mutex
var stateUpdated = make(chan time.Time, 16)

// 為 DPI 縮放所用，全域縮放因子（預設 1.0 表示 96 DPI）
var scale float64 = 1.0

// --------------------------
// 主函式
// --------------------------

func main() {
	// 與 Discord IPC 連線（細節不變）
	conn, response, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://127.0.0.1:6463/?v=1&client_id=%s&encoding=json", clientID), http.Header{"Origin": []string{apiURL}})
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	fmt.Println(response)

	file, err := os.ReadFile("token.txt")
	var m = map[string]any{}
	go LoopImage() // 啟動圖片更新與繪製的 goroutine

	for {
		err := conn.ReadJSON(&m)
		if err != nil {
			panic(err)
		}

		var cmd string
		var evt string
		if cmdS, ok := m["cmd"]; ok && cmdS != nil {
			cmd = cmdS.(string)
		}
		if evtS, ok := m["evt"]; ok && evtS != nil {
			evt = evtS.(string)
		}

		switch cmd {
		case "GET_SELECTED_VOICE_CHANNEL":
			if currentChannel != "" {
				for _, sub := range subs {
					SUBSCRIBE := map[string]any{
						"nonce": uuid.NewString(),
						"evt":   sub,
						"args": map[string]any{
							"channel_id": currentChannel,
						},
						"cmd": "UNSUBSCRIBE",
					}
					conn.WriteJSON(SUBSCRIBE)
				}

				currentChannel = ""
			}
			var data ChannelStates
			err := mapstructure.Decode(m["data"], &data)
			currentChannel = data.Id
			fmt.Println(err, "SELECTED_VOICE_CHANNEL", data)
			userMapLock.Lock()
			for _, vs := range data.VoiceStates {
				userMap[vs.User.Id] = &vs
			}
			userMapLock.Unlock()

			for _, sub := range subs {
				SUBSCRIBE := map[string]any{
					"nonce": uuid.NewString(),
					"evt":   sub,
					"args": map[string]any{
						"channel_id": data.Id,
					},
					"cmd": "SUBSCRIBE",
				}
				conn.WriteJSON(SUBSCRIBE)
			}
			NotifyUpdate()
		case "AUTHENTICATE":
			if evt == "ERROR" {
				conn.WriteJSON(Authorize{
					Nonce: uuid.NewString(),
					Cmd:   "AUTHORIZE",
					AuthorizeArgs: AuthorizeArgs{
						ClientId: clientID,
						Scopes:   []string{"identify", "rpc"},
					},
				})
				continue
			}

			data := m["data"].(map[string]any)
			var user User
			mapstructure.Decode(data["user"], &user)
			selfID = user.Id

			SUBSCRIBE_VOICE_CHANNEL_SELECT := map[string]any{
				"nonce": uuid.NewString(),
				"evt":   "VOICE_CHANNEL_SELECT",
				"cmd":   "SUBSCRIBE",
			}
			conn.WriteJSON(SUBSCRIBE_VOICE_CHANNEL_SELECT)

			GET_VOICE_CHANNEL_SELECT := map[string]any{
				"nonce": uuid.NewString(),
				"cmd":   "GET_SELECTED_VOICE_CHANNEL",
			}
			conn.WriteJSON(GET_VOICE_CHANNEL_SELECT)
		case "AUTHORIZE":
			code := m["data"].(map[string]any)["code"].(string)
			token := RemoteLogin(code)
			os.WriteFile("token.txt", []byte(token), os.ModePerm)

			AUTHENTICATE := map[string]any{
				"cmd": "AUTHENTICATE",
				"args": map[string]any{
					"access_token": token,
				},
				"nonce": uuid.NewString(),
			}
			conn.WriteJSON(AUTHENTICATE)
		case "DISPATCH":
			switch evt {
			case "VOICE_STATE_DELETE":
				var data UserState
				err := mapstructure.Decode(m["data"], &data)
				fmt.Println(err, "VOICE_STATE_DELETE", data)
				userMapLock.Lock()
				delete(userMap, data.User.Id)
				userMapLock.Unlock()
				NotifyUpdate()
			case "VOICE_STATE_CREATE":
				var data UserState
				err := mapstructure.Decode(m["data"], &data)
				fmt.Println(err, "VOICE_STATE_CREATE", data)
				userMapLock.Lock()
				userMap[data.User.Id] = &data
				userMapLock.Unlock()
				NotifyUpdate()
			case "VOICE_STATE_UPDATE":
				var data UserState
				err := mapstructure.Decode(m["data"], &data)
				fmt.Println(err, "VOICE_STATE_UPDATE", data)
				userMapLock.Lock()
				userMap[data.User.Id] = &data
				userMapLock.Unlock()
				NotifyUpdate()
			case "SPEAKING_START":
				s := m["data"].(map[string]any)["user_id"].(string)
				userMapLock.Lock()
				if _, ok := userMap[s]; ok {
					userMap[s].VoiceState.Talking = true
				}
				userMapLock.Unlock()
				NotifyUpdate()
			case "VOICE_CHANNEL_SELECT":
				if currentChannel != "" {
					for _, sub := range subs {
						SUBSCRIBE := map[string]any{
							"nonce": uuid.NewString(),
							"evt":   sub,
							"args": map[string]any{
								"channel_id": currentChannel,
							},
							"cmd": "UNSUBSCRIBE",
						}
						conn.WriteJSON(SUBSCRIBE)
					}

					currentChannel = ""
				}
				userMapLock.Lock()
				userMap = map[string]*UserState{}
				userMapLock.Unlock()
				currentChannel = m["data"].(map[string]any)["channel_id"].(string)
				GET_VOICE_CHANNEL_SELECT := map[string]any{
					"nonce": uuid.NewString(),
					"cmd":   "GET_SELECTED_VOICE_CHANNEL",
				}
				conn.WriteJSON(GET_VOICE_CHANNEL_SELECT)
			case "SPEAKING_STOP":
				s := m["data"].(map[string]any)["user_id"].(string)
				userMapLock.Lock()
				if _, ok := userMap[s]; ok {
					userMap[s].VoiceState.Talking = false
				}
				userMapLock.Unlock()
				NotifyUpdate()
			case "READY":
				if file == nil {
					conn.WriteJSON(Authorize{
						Nonce: uuid.NewString(),
						Cmd:   "AUTHORIZE",
						AuthorizeArgs: AuthorizeArgs{
							ClientId: clientID,
							Scopes:   []string{"identify", "rpc"},
						},
					})
				} else {
					AUTHENTICATE := map[string]any{
						"cmd": "AUTHENTICATE",
						"args": map[string]any{
							"access_token": string(file),
						},
						"nonce": uuid.NewString(),
					}
					conn.WriteJSON(AUTHENTICATE)
				}
			}
		}
	}
}

func StringOr(s any, or string) string {
	if s, ok := s.(string); ok {
		return s
	}
	return or
}

func NotifyUpdate() {
	for len(stateUpdated) > 0 {
		<-stateUpdated
	}
	stateUpdated <- time.Now()
}

func RemoteLogin(code string) string {
	resp, err := http.Post(apiURL+"/token", "application/json", strings.NewReader(fmt.Sprintf("{\"code\": \"%s\"}", code)))
	if err != nil {
		panic(err)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(resp.Body).Decode(&t)
	return t.AccessToken
}

func StrToUnsafePointer(ostr string) uintptr {
	str, err := syscall.UTF16PtrFromString(ostr)
	if err != nil {
		panic(err)
	}
	return uintptr(unsafe.Pointer(str))
}

// --------------------------
// 與 GDI+、User32 相關的 DLL 與函數宣告
// --------------------------

var gdiplusDLL = syscall.NewLazyDLL("gdiplus.dll")
var user32DLL = syscall.NewLazyDLL("user32.dll")
var gdipLoadImageFromStream = gdiplusDLL.NewProc("GdipLoadImageFromStream")
var gdipSetCompositingMode = gdiplusDLL.NewProc("GdipSetCompositingMode")
var gdipDrawImageI = gdiplusDLL.NewProc("GdipDrawImageI")
var gdipCreateFromHDC = gdiplusDLL.NewProc("GdipCreateFromHDC")
var gdipGraphicsClear = gdiplusDLL.NewProc("GdipGraphicsClear")
var gdipDeleteGraphics = gdiplusDLL.NewProc("GdipDeleteGraphics")
var gdipDisposeImage = gdiplusDLL.NewProc("GdipDisposeImage")
var gdipFree = gdiplusDLL.NewProc("GdipFree")

// --------------------------
// LoopImage 函式：處理圖片更新與繪圖，同時根據 DPI 縮放
// --------------------------

func LoopImage() {
	// 建立暫存資料夾
	os.Mkdir("avatars", os.ModePerm)
	runtime.LockOSThread()
	w32.CoInitialize()
	defer w32.CoUninitialize()

	taskBar := w32.FindWindow("Shell_TrayWnd", "")
	// 取得 Start 按鈕視窗
	findWindowExW := user32DLL.NewProc("FindWindowExW")
	shcoreDLL := windows.NewLazySystemDLL("shcore.dll")
	setProcessDpiAwareness := shcoreDLL.NewProc("SetProcessDpiAwareness")
	// 設定為 Per Monitor DPI Aware（2）
	setProcessDpiAwareness.Call(uintptr(2))
	// 取得 GDI+ 初始化
	gdiInput := gdiplus.GdiplusStartupInput{
		GdiplusVersion: 1,
	}
	gdiOutput := gdiplus.GdiplusStartupOutput{}
	startup := gdiplus.GdiplusStartup(&gdiInput, &gdiOutput)
	if startup != gdiplus.Ok {
		panic(startup)
	}

	taskBarWND := win.HWND(taskBar)
	hStart, _, _ := findWindowExW.Call(uintptr(taskBar), 0, StrToUnsafePointer("Start"), 0)
	hStartWND := win.HWND(hStart)

	// 取得 GetDpiForWindow 函數（Windows 10 以上可用）
	getDpiForWindow := user32DLL.NewProc("GetDpiForWindow")

	windowMain := ui.NewWindowMain(
		ui.WindowMainOpts().WndStyles(co.WS_POPUP | co.WS_SYSMENU).
			WndExStyles(co.WS_EX_TOOLWINDOW | co.WS_EX_TOPMOST).
			HBrushBkgnd(win.HBRUSH(0)),
	)

	// WM_CREATE：設定視窗屬性，並根據 DPI 計算縮放因子
	windowMain.On().WmCreate(func(p wm.Create) int {
		windowMain.Hwnd().SetParent(taskBarWND)

		exstyle := co.WS_EX(windowMain.Hwnd().GetWindowLongPtr(co.GWLP_EXSTYLE))
		windowMain.Hwnd().SetWindowLongPtr(co.GWLP_EXSTYLE, uintptr(exstyle|co.WS_EX_LAYERED))
		windowMain.Hwnd().SetLayeredWindowAttributes(win.RGB(0, 0, 0), 0, co.LWA_COLORKEY)
		windowMain.Hwnd().ShowWindow(co.SW_SHOW)

		// 取得目前視窗 DPI，計算縮放因子（以 96 DPI 為基準）
		dpi, _, _ := getDpiForWindow.Call(uintptr(windowMain.Hwnd()))
		scale = float64(dpi) / 96.0

		hLeft := hStartWND.GetWindowRect().Left
		// 原本固定寬度 50 與高度 48，乘上 scale 進行縮放
		scaledWidth := int32(50 * scale)
		scaledHeight := int32(48 * scale)
		windowMain.Hwnd().MoveWindow(hLeft-scaledWidth, 0, scaledWidth, scaledHeight, true)
		return 0
	})
	windowMain.On().WmMove(func(p wm.Move) {
		// 視窗移動時，重新計算縮放因子
		dpi, _, _ := getDpiForWindow.Call(uintptr(windowMain.Hwnd()))
		scale = float64(dpi) / 96.0
	})

	var imgPtr uintptr
	var avatarList []string
	lastMemberCount := 0
	userImageMap := map[string]image.Image{}
	userImageLock := sync.Mutex{}
	downloading := []string{}
	needGreen := []string{}
	needRed := []string{}
	needRedBackground := []string{}

	buffer := new(bytes.Buffer)
	hLeft := hStartWND.GetWindowRect().Left
	clearColor := gdiplus.MakeARGB(0, 0, 0, 0)

	// WM_PAINT：根據縮放因子調整所有尺寸
	windowMain.On().WmPaint(func() {
		runtime.LockOSThread()

		windowMain.RunUiThread(func() {
			avatarSize := int(48 * scale)
			gap := int(2 * scale)
			totalWidth := (avatarSize + gap) * len(avatarList)
			output := image.NewRGBA(image.Rect(0, 0, totalWidth, avatarSize))

			buffer.Reset()
			avatarList = nil
			needRed = nil
			needGreen = nil
			needRedBackground = nil
			userMapLock.Lock()
			for _, user := range userMap {
				pat := "avatars/" + user.User.Id + "-" + user.User.Avatar + ".png"
				if _, err := os.Stat(pat); errors.Is(err, os.ErrNotExist) && !slices.Contains(downloading, pat) {
					go func() {
						resp, err := http.Get("https://cdn.discordapp.com/avatars/" + user.User.Id + "/" + user.User.Avatar + ".png?size=128")
						if err != nil {
							return
						}
						file, err := os.Create(pat)
						if err != nil {
							return
						}
						r := io.TeeReader(resp.Body, file)
						if err != nil {
							return
						}
						userImageLock.Lock()
						img, err := png.Decode(r)

						scaledImg := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
						draw2.CatmullRom.Scale(scaledImg, scaledImg.Bounds(), img, img.Bounds(), draw.Over, nil)

						userImageMap[pat] = scaledImg
						userImageLock.Unlock()
					}()
					continue
				}
				if user.Mute {
					continue
				}
				if user.VoiceState.Talking {
					needGreen = append(needGreen, pat)
				}
				if user.VoiceState.Mute || user.VoiceState.SelfMute || user.VoiceState.Deaf || user.VoiceState.SelfDeaf {
					needRed = append(needRed, pat)
				}
				if user.VoiceState.Deaf || user.VoiceState.SelfDeaf {
					needRedBackground = append(needRedBackground, pat)
				}
				avatarList = append(avatarList, pat)
			}
			for _, s := range avatarList {
				if _, ok := userImageMap[s]; ok {
					continue
				}
				if _, err := os.Stat(s); err != nil {
					continue
				}
				file, err := os.Open(s)
				if err != nil {
					continue
				}
				img, err := png.Decode(file)
				if err != nil {
					continue
				}

				scaledImg := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
				draw2.CatmullRom.Scale(scaledImg, scaledImg.Bounds(), img, img.Bounds(), draw.Over, nil)

				userImageMap[s] = scaledImg
			}
			userMapLock.Unlock()

			if imgPtr != 0 {
				_, _, _ = gdipDisposeImage.Call(imgPtr)
			}

			if len(avatarList) == 0 {
				time.Sleep(50 * time.Millisecond)
				return
			}

			// 使用 DPI 縮放調整尺寸：原本固定 48 與 2，乘上 scale 得到 avatarSize 與間隔 gap
			sort.Strings(avatarList)
			for i, s := range avatarList {
				if _, ok := userImageMap[s]; !ok {
					continue
				}
				img := userImageMap[s]
				// 創建一個圓形遮罩（依據縮放後的尺寸）
				mask := image.NewAlpha(image.Rect(0, 0, avatarSize, avatarSize))
				for y := 0; y < avatarSize; y++ {
					for x := 0; x < avatarSize; x++ {
						dx := float64(x) - float64(avatarSize)/2
						dy := float64(y) - float64(avatarSize)/2
						if math.Sqrt(dx*dx+dy*dy) <= float64(avatarSize)/2 {
							mask.SetAlpha(x, y, color.Alpha{255})
						}
					}
				}

				// 將圖片與遮罩繪製到 output 上
				destRect := image.Rect(i*(avatarSize+gap), 0, i*(avatarSize+gap)+avatarSize, avatarSize)
				draw.DrawMask(output, destRect, img, image.Point{}, mask, image.Point{}, draw.Over)

				if slices.Contains(needRed, s) {
					// 畫紅色邊框
					drawCircle(output, i*(avatarSize+gap), 0, avatarSize, color.RGBA{237, 66, 69, 255})
				}
				if slices.Contains(needGreen, s) {
					// 畫綠色邊框
					drawCircle(output, i*(avatarSize+gap), 0, avatarSize, color.RGBA{87, 242, 135, 255})
				}
				if slices.Contains(needRedBackground, s) {
					// 畫較粗的紅色邊框
					drawCircleBold(output, i*(avatarSize+gap), 0, avatarSize, color.RGBA{237, 66, 69, 255})
				}
			}

			_ = png.Encode(buffer, output)
			memory, err := createIStreamFromMemory(buffer.Bytes())
			if err != nil {
				fmt.Println(err)
				return
			}

			_, _, _ = gdipLoadImageFromStream.Call(
				uintptr(unsafe.Pointer(memory)),
				uintptr(unsafe.Pointer(&imgPtr)),
			)
			memory.Release()

			if hLeft != hStartWND.GetWindowRect().Left || lastMemberCount != len(avatarList) {
				hLeft = hStartWND.GetWindowRect().Left
				lastMemberCount = len(avatarList)
				windowMain.Hwnd().MoveWindow(hLeft-int32(float64(50*len(avatarList))*scale), 0, int32(float64(50*len(avatarList))*scale), int32(48*scale), true)
			}

			var ps win.PAINTSTRUCT
			hdc := windowMain.Hwnd().BeginPaint(&ps)
			var graphics uintptr
			_, _, _ = gdipCreateFromHDC.Call(uintptr(hdc), uintptr(unsafe.Pointer(&graphics)))
			_, _, _ = gdipGraphicsClear.Call(graphics, uintptr(clearColor))
			_, _, _ = gdipDrawImageI.Call(
				graphics,
				imgPtr,
				uintptr(0),
				uintptr(0))
			_, _, _ = gdipDeleteGraphics.Call(graphics)
			windowMain.Hwnd().EndPaint(&ps)
		})
	})

	run := sync.OnceFunc(func() {
		go windowMain.RunAsMain()
	})

	for {
		select {
		case <-stateUpdated:
		case <-time.After(time.Second):

		}
		run()
		windowMain.Hwnd().InvalidateRect(nil, true)
	}

	gdiplus.GdiplusShutdown()
}

// --------------------------
// IStream 相關函式
// --------------------------

var shCreateMemStream = syscall.NewLazyDLL("shlwapi.dll").NewProc("SHCreateMemStream")

func createIStreamFromMemory(data []byte) (*w32.IStream, error) {
	ptr := uintptr(0)
	if len(data) > 0 {
		ptr = uintptr(unsafe.Pointer(&data[0]))
	}
	ret, _, _ := shCreateMemStream.Call(ptr, uintptr(len(data)))
	if ret == 0 {
		return nil, fmt.Errorf("failed to create IStream")
	}
	return (*w32.IStream)(unsafe.Pointer(ret)), nil
}

func createIStreamUsingHGlobal(data []byte) (*w32.IStream, w32.HGLOBAL, error) {
	hGlobal := w32.GlobalAlloc(0, uint32(len(data)))
	mem := w32.GlobalLock(hGlobal)
	copy((*[1 << 30]byte)(mem)[:], data)
	w32.GlobalUnlock(hGlobal)
	stream, hresult := w32.CreateStreamOnHGlobal(hGlobal, true)
	if hresult != 0 {
		w32.GlobalFree(hGlobal)
		return nil, 0, fmt.Errorf("CreateStreamOnHGlobal failed")
	}
	return stream, hGlobal, nil
}

// --------------------------
// 畫圓形邊框函式：根據 DPI 縮放調整邊框寬度
// --------------------------

func drawCircle(dst draw.Image, x, y, diameter int, clr color.Color) {
	radius := diameter / 2
	centerX := x + radius
	centerY := y + radius
	borderWidth := int(2 * scale) // 依據 DPI 縮放
	for i := 0; i < 360; i++ {
		angle := float64(i) * math.Pi / 180
		for r := radius - borderWidth; r <= radius; r++ {
			dx := float64(r) * math.Cos(angle)
			dy := float64(r) * math.Sin(angle)
			dst.Set(centerX+int(dx), centerY+int(dy), clr)
		}
	}
}

func drawCircleBold(dst draw.Image, x, y, diameter int, clr color.Color) {
	radius := diameter / 2
	centerX := x + radius
	centerY := y + radius
	borderWidth := int(4 * scale)
	for i := 0; i < 360; i++ {
		angle := float64(i) * math.Pi / 180
		for r := radius - borderWidth; r <= radius; r++ {
			dx := float64(r) * math.Cos(angle)
			dy := float64(r) * math.Sin(angle)
			dst.Set(centerX+int(dx), centerY+int(dy), clr)
		}
	}
}
