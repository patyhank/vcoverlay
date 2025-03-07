package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/fasthttp/websocket"
	"github.com/golang/freetype/truetype"
	"github.com/gonutz/w32/v2"
	"github.com/google/uuid"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/ui/wm"
	"github.com/rodrigocfd/windigo/win"
	"github.com/rodrigocfd/windigo/win/co"
	"github.com/shahfarhadreza/go-gdiplus"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/windows"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"maps"
	"math"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

//go:embed assets/deaf.png
var deafData []byte

//go:embed assets/mute.png
var muteData []byte

//go:embed assets/NotoSansTC.ttf
var fontData []byte

// --------------------------
// 資料結構與全域變數
// --------------------------
var self struct {
	ChannelID string
	UserID    string
}

var apiURL = "https://api.overlayed.dev"
var clientID = "905987126099836938"

var subs = []EventType{
	EventSpeakingStart,
	EventSpeakingStop,
	EventVoiceStateCreate,
	EventVoiceStateDelete,
	EventVoiceStateUpdate,
}

var userMap = map[string]*UserState{}
var userMapLock sync.Mutex

var stateUpdated = make(chan time.Time, 16)

// 為 DPI 縮放所用，全域縮放因子（預設 1.0 表示 96 DPI）
// 為 DPI 縮放所用，全域縮放因子（預設 1.0 表示 96 DPI）
var scale float64 = 1.0

const messageDuration = 300

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
	if response.StatusCode != 101 {
		fmt.Println(response)
	}

	tokenData, err := os.ReadFile("token.txt")
	go LoopImage() // 啟動圖片更新與繪製的 goroutine

	for {
		var payload MessagePayload
		_, p, err := conn.ReadMessage()
		if err != nil {
			fmt.Println(err)
			time.Sleep(5 * time.Second)
			continue
		}

		err = json.Unmarshal(p, &payload)
		if err != nil {
			fmt.Println(err)
			continue
		}

		err = payload.Resolve()
		if err != nil {
			fmt.Println(err)
			continue
		}

		switch data := payload.ResolvedData.(type) {
		case *ReadyInfo:
			if tokenData == nil {
				conn.WriteJSON(CmdRequest{
					Args: AuthorizeArgs{
						ClientId: clientID,
						Scopes:   []string{"identify", "rpc"},
					},
					Cmd:   CommandAuthorize,
					Nonce: uuid.NewString(),
				})
			} else {
				conn.WriteJSON(CmdRequest{
					Args: AuthenticateArgs{
						AccessToken: string(tokenData),
					},
					Cmd:   CommandAuthenticate,
					Nonce: uuid.NewString(),
				})
			}
			log.Println("Ready as ", data.User.Username)
		case *AuthenticatePayload:
			if payload.Evt == "ERROR" {
				conn.WriteJSON(CmdRequest{
					Args: AuthorizeArgs{
						ClientId: clientID,
						Scopes:   []string{"identify", "rpc"},
					},
					Cmd:   CommandAuthorize,
					Nonce: uuid.NewString(),
				})
				continue
			}

			self.UserID = data.User.Id
			conn.WriteJSON(CmdRequest{
				Cmd:   CommandSubscribe,
				Evt:   EventVoiceChannelSelect,
				Nonce: uuid.NewString(),
			})

			conn.WriteJSON(CmdRequest{
				Nonce: uuid.NewString(),
				Cmd:   CommandGetSelectedVoiceChannel,
			})
		case *AuthorizePayload:
			token := RemoteLogin(data.Code)
			os.WriteFile("token.txt", []byte(token), os.ModePerm)

			conn.WriteJSON(CmdRequest{
				Args: AuthenticateArgs{
					AccessToken: token,
				},
				Cmd:   CommandAuthenticate,
				Nonce: uuid.NewString(),
			})
		case *VoiceChannel:
			if self.ChannelID != "" {
				for _, sub := range subs {
					conn.WriteJSON(CmdRequest{
						Args: Args{
							ChannelID: self.ChannelID,
						},
						Cmd:   CommandUnsubscribe,
						Nonce: uuid.NewString(),
						Evt:   sub,
					})
				}
			}
			if data.Id == "" {
				continue
			}

			self.ChannelID = data.Id

			userMapLock.Lock()
			userMap = make(map[string]*UserState)

			for _, vs := range data.VoiceStates {
				userMap[vs.User.Id] = &vs
			}
			userMapLock.Unlock()

			for _, sub := range subs {
				marshal, _ := json.Marshal(CmdRequest{
					Args: Args{
						ChannelID: data.Id,
					},
					Cmd:   CommandSubscribe,
					Nonce: uuid.NewString(),
					Evt:   sub,
				})
				fmt.Println(string(marshal), data.Id)
				conn.WriteJSON(CmdRequest{
					Args: Args{
						ChannelID: data.Id,
					},
					Cmd:   CommandSubscribe,
					Nonce: uuid.NewString(),
					Evt:   sub,
				})
			}
		case *UserState:
			if data.User.Id == "" {
				continue
			}
			switch payload.Evt {
			case EventVoiceStateDelete:
				userMapLock.Lock()
				userMap[data.User.Id].Left = true
				userMap[data.User.Id].LeftTick = messageDuration
				messageImg := getTextedImage(fmt.Sprintf("離開"))
				userMap[data.User.Id].StateImage = messageImg
				userMapLock.Unlock()
				NotifyLongUpdate(data.User.Id)
			case EventVoiceStateCreate:
				userMapLock.Lock()
				var internalState InternalState
				if userMap[data.User.Id] != nil && userMap[data.User.Id].InternalState != EmptyInternalState {
					internalState = userMap[data.User.Id].InternalState
					internalState.LeftTick = 0
					internalState.Left = false
				}
				userMap[data.User.Id] = data
				userMap[data.User.Id].InternalState = internalState
				userMap[data.User.Id].Joined = true
				userMap[data.User.Id].JoinTick = messageDuration

				messageImg := getTextedImage(fmt.Sprintf("加入"))
				userMap[data.User.Id].StateImage = messageImg
				userMapLock.Unlock()
				NotifyLongUpdate(data.User.Id)
			case EventVoiceStateUpdate:
				userMapLock.Lock()
				var internalState InternalState
				if userMap[data.User.Id] != nil && userMap[data.User.Id].InternalState != EmptyInternalState {
					internalState = userMap[data.User.Id].InternalState
				}
				userMap[data.User.Id] = data
				userMap[data.User.Id].InternalState = internalState
				userMapLock.Unlock()

			}
			NotifyUpdate()

		case *UserIdInfo:
			switch payload.Evt {
			case EventSpeakingStart:
				userMapLock.Lock()
				if _, ok := userMap[data.UserId]; ok {
					userMap[data.UserId].Talking = true
				}
				userMapLock.Unlock()
			case EventSpeakingStop:
				userMapLock.Lock()
				if _, ok := userMap[data.UserId]; ok {
					userMap[data.UserId].Talking = false
				}
				userMapLock.Unlock()
			}
			NotifyUpdate()

		}
	}
}

func NotifyUpdate() {
	for len(stateUpdated) > 0 {
		<-stateUpdated
	}
	stateUpdated <- time.Now()
}

var longUpdateLock sync.Mutex

func NotifyLongUpdate(userId string) {
	go func() {
		longUpdateLock.Lock()
		defer longUpdateLock.Unlock()
		ticker := time.NewTicker(time.Millisecond * 1)
		for {
			for len(stateUpdated) > 0 {
				<-stateUpdated
			}

			stateUpdated <- time.Now()
			<-ticker.C
			userMapLock.Lock()
			if user, ok := userMap[userId]; ok {
				userMapLock.Unlock()
				if user.Left || user.Joined {
					continue
				}
			} else {
				userMapLock.Unlock()
			}
			break
		}

		ticker.Stop()
	}()
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
	hTrayNotify, _, _ := findWindowExW.Call(uintptr(taskBar), 0, StrToUnsafePointer("TrayNotifyWnd"), 0)
	hTrayNotifyWND := win.HWND(hTrayNotify)

	targetHWND := hStartWND
	if targetHWND.GetWindowRect().Left == 0 {
		targetHWND = hTrayNotifyWND
	}

	// 取得 GetDpiForWindow 函數（Windows 10 以上可用）
	getDpiForWindow := user32DLL.NewProc("GetDpiForWindow")

	windowMain := ui.NewWindowMain(
		ui.WindowMainOpts().WndStyles(co.WS_POPUP | co.WS_SYSMENU).
			WndExStyles(co.WS_EX_TOOLWINDOW | co.WS_EX_TOPMOST | co.WS_EX_LAYERED).
			HBrushBkgnd(win.HBRUSH(0)),
	)

	// WM_CREATE：設定視窗屬性，並根據 DPI 計算縮放因子
	windowMain.On().WmCreate(func(p wm.Create) int {
		windowMain.Hwnd().SetParent(taskBarWND)

		windowMain.Hwnd().SetLayeredWindowAttributes(win.RGB(0, 0, 0), 0, co.LWA_COLORKEY)
		windowMain.Hwnd().ShowWindow(co.SW_SHOW)

		// 取得目前視窗 DPI，計算縮放因子（以 96 DPI 為基準）
		dpi, _, _ := getDpiForWindow.Call(uintptr(windowMain.Hwnd()))
		scale = float64(dpi) / 96.0

		hLeft := targetHWND.GetWindowRect().Left
		// 原本固定寬度 50 與高度 48，乘上 scale 進行縮放
		scaledWidth := int32(50 * scale)
		scaledHeight := int32(48 * scale)
		if err := loadFont(48 * scale); err != nil {
			log.Println(err)
		}
		windowMain.Hwnd().MoveWindow(hLeft-scaledWidth, 0, scaledWidth, scaledHeight, true)
		return 0
	})
	windowMain.On().WmMove(func(p wm.Move) {
		// 視窗移動時，重新計算縮放因子
		dpi, _, _ := getDpiForWindow.Call(uintptr(windowMain.Hwnd()))
		scale = float64(dpi) / 96.0
	})

	var imgPtr uintptr

	buffer := new(bytes.Buffer)
	hLeft := targetHWND.GetWindowRect().Left
	clearColor := gdiplus.MakeARGB(0, 0, 0, 0)

	var lastBound image.Rectangle
	// WM_PAINT：根據縮放因子調整所有尺寸
	windowMain.On().WmPaint(func() {
		runtime.LockOSThread()

		windowMain.RunUiThread(func() {

			if imgPtr != 0 {
				_, _, _ = gdipDisposeImage.Call(imgPtr)
			}
			buffer.Reset()

			var imgList []image.Image
			userMapLock.Lock()
			userKey := slices.Collect(maps.Keys(userMap))
			slices.Sort(userKey)

			for _, s := range userKey {
				state := userMap[s]
				if state.Left && state.LeftTick <= -messageDuration && state.StateImage == nil {
					delete(userMap, s)
					continue
				}
				avatar, ok := <-getAvatar(state)
				if !ok {
					continue
				}

				imgList = append(imgList, getPaintedAvatar(state, avatar))
			}
			userMapLock.Unlock()

			merged := mergeImages(imgList)

			_ = png.Encode(buffer, merged)
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

			bounds := merged.Bounds()
			if hLeft != targetHWND.GetWindowRect().Left || bounds != lastBound {
				hLeft = targetHWND.GetWindowRect().Left
				lastBound = bounds

				windowMain.Hwnd().MoveWindow(hLeft-int32(float64(bounds.Dx())), 0, int32(float64(bounds.Dx())), int32(float64(bounds.Dy())), true)
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

func resizeImage(targetSize float64, img image.Image) *image.RGBA {
	avatarSize := int(targetSize * scale)
	scaledImg := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	xdraw.CatmullRom.Scale(scaledImg, scaledImg.Bounds(), img, img.Bounds(), draw.Over, nil)

	return scaledImg
}

// darkenImage 將圖片中每個像素的 RGB 值乘上 factor (例如 0.5) 來變暗
func darkenImage(img image.Image, factor float64) *image.RGBA {
	bounds := img.Bounds()
	darkened := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			// 取得原始像素顏色
			originalColor := img.At(x, y)
			r, g, b, a := originalColor.RGBA()

			// 由於 RGBA() 回傳的值範圍是 0~65535，
			// 所以先轉換到 0~255（右移 8 位元），再乘上暗化因子
			dr := uint8(float64(r>>8) * factor)
			dg := uint8(float64(g>>8) * factor)
			db := uint8(float64(b>>8) * factor)
			da := uint8(a >> 8)

			// 設定新的像素值
			darkened.Set(x, y, color.RGBA{R: dr, G: dg, B: db, A: da})
		}
	}
	return darkened
}

func mergeImages(imgList []image.Image) *image.RGBA {
	// 計算合併後圖片的總寬度和最大高度
	totalWidth := 0
	maxHeight := 0
	for _, img := range imgList {
		bounds := img.Bounds()
		totalWidth += bounds.Dx() // 累加寬度
		if bounds.Dy() > maxHeight {
			maxHeight = bounds.Dy() // 取得最大高度
		}
	}

	// 建立一張新的 RGBA 圖片作為合併後的結果
	merged := image.NewRGBA(image.Rect(0, 0, totalWidth, maxHeight))

	// 設定每張圖片放置的偏移位置
	offsetX := 0
	for _, img := range imgList {
		bounds := img.Bounds()
		// 設定來源與目的區域（這裡直接從 (0,0) 複製整張圖片）
		dstRect := image.Rect(offsetX, 0, offsetX+bounds.Dx(), bounds.Dy())
		draw.Draw(merged, dstRect, img, bounds.Min, draw.Over)
		offsetX += bounds.Dx() // 更新偏移量
	}
	return merged
}

var deafImage *image.RGBA
var muteImage *image.RGBA

func getPaintedAvatar(userState *UserState, oImg *image.RGBA) *image.RGBA {
	oImg = resizeImage(48, oImg)

	if deafImage == nil {
		deafImg, _, _ := image.Decode(bytes.NewReader(deafData))
		deafImage = resizeImage(32, deafImg)
	}
	if muteImage == nil {
		muteImg, _, _ := image.Decode(bytes.NewReader(muteData))
		muteImage = resizeImage(32, muteImg)
	}
	imgSize := oImg.Bounds().Dx()
	// 創建一個圓形遮罩（依據縮放後的尺寸）
	mask := image.NewAlpha(oImg.Bounds())
	for y := 0; y < imgSize; y++ {
		for x := 0; x < imgSize; x++ {
			dx := float64(x) - float64(imgSize)/2
			dy := float64(y) - float64(imgSize)/2
			if math.Sqrt(dx*dx+dy*dy) <= float64(imgSize)/2 {
				mask.SetAlpha(x, y, color.Alpha{255})
			}
		}
	}

	img := image.NewRGBA(oImg.Bounds())

	draw.DrawMask(img, img.Bounds(), oImg, image.Point{}, mask, image.Point{}, draw.Over)

	if userState.VoiceState.Mute || userState.VoiceState.SelfMute {
		img = darkenImage(img, 0.5)
		draw.Draw(img, img.Bounds(), muteImage, image.Pt(-9, -9), draw.Over)
		drawCircle(img, 0, 0, imgSize, color.RGBA{237, 66, 69, 255})
	}

	if userState.VoiceState.Deaf || userState.VoiceState.SelfDeaf {
		img = darkenImage(img, 0.5)
		draw.Draw(img, img.Bounds(), deafImage, image.Pt(-9, -9), draw.Over)
		drawCircle(img, 0, 0, imgSize, color.RGBA{237, 66, 69, 255})
	}

	if userState.Talking {
		drawCircle(img, 0, 0, imgSize, color.RGBA{87, 242, 135, 255})
	}

	if userState.Left && userState.StateImage != nil {
		img = darkenImage(img, 0.8)

		stateImage := userState.StateImage

		if userState.LeftTick >= 0 {
			progress := float64(messageDuration-userState.LeftTick) / float64(messageDuration)
			visibleWidth := int(progress * float64(stateImage.Bounds().Dx()))
			rect := image.Rect(stateImage.Bounds().Dx()-visibleWidth, 0, stateImage.Bounds().Dx(), stateImage.Bounds().Dy())
			frame := stateImage.SubImage(rect)

			img = mergeImages([]image.Image{img, frame})
		} else {
			stateImage = mergeImages([]image.Image{img, stateImage})

			progress := float64(-userState.LeftTick) / float64(messageDuration)
			visibleWidth := stateImage.Bounds().Dx() - int(progress*float64(stateImage.Bounds().Dx()))
			rect := image.Rect(0, 0, visibleWidth, stateImage.Bounds().Dy())
			img = stateImage.SubImage(rect).(*image.RGBA)
		}

		userState.LeftTick -= 1
		if userState.LeftTick <= -messageDuration {
			userState.StateImage = nil
		}
	}

	if userState.Joined && userState.StateImage != nil {
		stateImage := userState.StateImage

		var rect image.Rectangle
		if userState.JoinTick >= 0 {
			progress := float64(messageDuration-userState.JoinTick) / float64(messageDuration)
			visibleWidth := int(progress * float64(stateImage.Bounds().Dx()))
			rect = image.Rect(stateImage.Bounds().Dx()-visibleWidth, 0, stateImage.Bounds().Dx(), stateImage.Bounds().Dy())
		} else {
			progress := float64(-userState.JoinTick) / float64(messageDuration)
			visibleWidth := stateImage.Bounds().Dx() - int(progress*float64(stateImage.Bounds().Dx()))
			rect = image.Rect(0, 0, visibleWidth, stateImage.Bounds().Dy())
		}

		frame := stateImage.SubImage(rect)

		img = mergeImages([]image.Image{img, frame})
		userState.JoinTick -= 1

		if userState.JoinTick <= -messageDuration {
			userState.Joined = false
			userState.StateImage = nil
		}
	}

	return img
}

func getAvatar(userState *UserState) <-chan *image.RGBA {
	imgChan := make(chan *image.RGBA, 1)
	if userState.CachedImage != nil {
		imgChan <- userState.CachedImage
		return imgChan
	}

	avatarPath := "avatars/" + userState.User.Id + "-" + userState.User.Avatar + ".png"
	avatarURL := "https://cdn.discordapp.com/avatars/" + userState.User.Id + "/" + userState.User.Avatar + ".png?size=128"
	if userState.User.Avatar == "" {
		i, _ := strconv.ParseInt(userState.User.Id, 10, 64)
		avatarPath = fmt.Sprintf("avatars/default-%d.png", (i>>22)%6)
		avatarURL = fmt.Sprintf("https://cdn.discordapp.com/embed/avatars/%d.png", (i>>22)%6)
	}

	file, err := os.ReadFile(avatarPath)
	if err == nil {
		img, _, err := image.Decode(bytes.NewReader(file))
		if err == nil {
			userState.CachedImage = resizeImage(48, img)
			imgChan <- userState.CachedImage
			return imgChan
		}
	}

	go func() {
		resp, err := http.Get(avatarURL)
		if err != nil {
			log.Println(err)
			return
		}

		file, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Println(err)
			return
		}

		img, _, err := image.Decode(bytes.NewReader(file))
		if err != nil {
			log.Println(err)
			return
		}

		err = os.WriteFile(avatarPath, file, os.ModePerm)
		if err != nil {
			log.Println(err)
			return
		}
		userState.CachedImage = resizeImage(48, img)
		imgChan <- userState.CachedImage
		NotifyUpdate()
	}()

	return imgChan
}

var fontFace font.Face

// loadFont 讀取 TTF 檔案並返回一個字型面孔（face）
func loadFont(fontSize float64) error {
	// 解析字體檔案
	f, err := truetype.Parse(fontData)
	if err != nil {
		return fmt.Errorf("解析字體失敗: %v", err)
	}
	// 建立字型面孔
	fontFace = truetype.NewFace(f, &truetype.Options{
		Size:    fontSize,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	return nil
}

// drawTextWithFont 使用指定字型在圖片上繪製文本
func drawTextWithFont(img *image.RGBA, x, y int, text string, face font.Face) {
	col := color.RGBA{R: 255, G: 255, B: 255, A: 255} // 白色字
	point := fixed.Point26_6{
		X: fixed.I(x),
		Y: fixed.I(y),
	}
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  point,
	}
	d.DrawString(text)
}

func getTextedImage(text string) *image.RGBA {
	if err := loadFont(48 * scale); err != nil {
		log.Println(err)
	}
	drawer := &font.Drawer{
		Face: fontFace,
	}

	boundsFixed := drawer.MeasureString(text)
	width := boundsFixed.Ceil()
	height := int(48 * scale)

	src := image.NewRGBA(image.Rect(0, 0, width+50, height))

	drawTextWithFont(src, 5, height-10, text, fontFace)

	return src
}
