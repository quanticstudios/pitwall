package mobile

/*
#include <jni.h>
int pitwallCamera(JavaVM *vm, jobject view, int start);
void pitwallBytes(JNIEnv *env, jbyteArray a, void *dst, jsize n);
*/
import "C"

import (
	"runtime"
	"sync"
	"time"
	"unsafe"

	"gioui.org/app"
	_ "gioui.org/app/permission/camera"
)

// The camera is QRScan.java, which scripts/apk.sh builds into a jar
// that gogio puts in the APK.
const canScan = true

var camera struct {
	sync.Mutex
	view  uintptr // the GioView, from the last ViewEvent
	frame func(frame)
	fail  func(string)
	last  time.Time // when the last frame went to frame
}

func setCameraView(e app.ViewEvent) {
	ae, ok := e.(app.AndroidViewEvent)
	if !ok {
		return
	}
	camera.Lock()
	camera.view = ae.View
	camera.Unlock()
	if ae.View == 0 {
		stopCamera()
	}
}

// startCamera asks for the camera permission if pitwall lacks it, then
// hands onFrame the back camera's frames, up to 15 a second, from the
// camera's thread. onFail gets why the camera stopped on its own, ""
// when the app went to the background.
func startCamera(onFrame func(frame), onFail func(string)) {
	camera.Lock()
	camera.frame, camera.fail = onFrame, onFail
	view := camera.view
	camera.Unlock()
	if view == 0 || callCamera(view, true) != 0 {
		stopCamera()
		onFail(failText[2])
	}
}

func stopCamera() {
	camera.Lock()
	camera.frame, camera.fail = nil, nil
	view := camera.view
	camera.Unlock()
	callCamera(view, false)
}

func callCamera(view uintptr, start bool) C.int {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	s := C.int(0)
	if start {
		s = 1
	}
	return C.pitwallCamera((*C.JavaVM)(unsafe.Pointer(app.JavaVM())), C.jobject(unsafe.Pointer(view)), s)
}

// failText is what the pairing screen says for QRScan.onFail's codes.
var failText = [...]string{
	"",
	"pitwall may not use the camera. Allow it in Android's settings for pitwall, or paste the link.",
	"The camera did not open. Paste the link instead.",
}

//export Java_com_quanticstudios_pitwall_QRScan_onFrame
func Java_com_quanticstudios_pitwall_QRScan_onFrame(env *C.JNIEnv, cls C.jclass, data C.jbyteArray, w, h, rot C.jint) {
	camera.Lock()
	on := camera.frame
	// why: frames come at up to 30 a second, each about a megabyte; a decode takes longer than that.
	if on == nil || time.Since(camera.last) < 66*time.Millisecond {
		camera.Unlock()
		return
	}
	camera.last = time.Now()
	camera.Unlock()
	y := make([]byte, int(w)*int(h)) // NV21 starts with the brightness plane
	C.pitwallBytes(env, data, unsafe.Pointer(&y[0]), C.jsize(len(y)))
	on(frame{y: y, w: int(w), h: int(h), rot: int(rot)})
}

//export Java_com_quanticstudios_pitwall_QRScan_onFail
func Java_com_quanticstudios_pitwall_QRScan_onFail(env *C.JNIEnv, cls C.jclass, why C.jint) {
	camera.Lock()
	fail := camera.fail
	camera.frame, camera.fail = nil, nil
	camera.Unlock()
	if fail != nil && int(why) < len(failText) {
		fail(failText[why])
	}
}
