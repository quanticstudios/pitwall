package com.quanticstudios.pitwall;

import android.Manifest;
import android.app.Activity;
import android.app.Application;
import android.app.Fragment;
import android.content.pm.PackageManager;
import android.graphics.SurfaceTexture;
import android.hardware.Camera;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.Surface;
import android.view.View;

// QRScan hands the back camera's preview frames to Go, for the pairing
// screen's Scan QR code (scan_android.go). It asks for the camera
// permission when a scan starts, and closes the camera when the app
// goes to the background.
// ponytail: android.hardware.Camera is deprecated but a third the code of
// Camera2 and works through API 35; move to Camera2 if Android drops it.
@SuppressWarnings("deprecation")
public final class QRScan {
	private static Camera cam;
	private static SurfaceTexture tex;
	private static boolean hooked;

	static native void onFrame(byte[] nv21, int w, int h, int rot);

	// onFail's why: 0 the app went to the background, 1 permission denied, 2 no camera.
	static native void onFail(int why);

	public static void start(final View view) {
		view.post(new Runnable() {
			public void run() {
				Activity a = (Activity) view.getContext();
				hook(a);
				if (a.checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
					open(a);
				} else {
					a.getFragmentManager().beginTransaction().add(new Ask(), "pitwall-camera").commitAllowingStateLoss();
				}
			}
		});
	}

	public static void stop() {
		new Handler(Looper.getMainLooper()).post(new Runnable() {
			public void run() {
				close();
			}
		});
	}

	// Ask holds the permission request, since only an Activity or a
	// Fragment gets its answer and GioActivity passes it on to no one.
	public static final class Ask extends Fragment {
		@Override public void onCreate(Bundle state) {
			super.onCreate(state);
			requestPermissions(new String[] {Manifest.permission.CAMERA}, 1);
		}

		@Override public void onRequestPermissionsResult(int code, String[] perms, int[] res) {
			Activity a = getActivity();
			getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
			if (res.length > 0 && res[0] == PackageManager.PERMISSION_GRANTED) {
				open(a);
			} else {
				onFail(1);
			}
		}
	}

	private static void open(Activity a) {
		close();
		try {
			Camera.CameraInfo info = new Camera.CameraInfo();
			int id = -1;
			for (int i = 0; i < Camera.getNumberOfCameras(); i++) {
				Camera.getCameraInfo(i, info);
				if (info.facing == Camera.CameraInfo.CAMERA_FACING_BACK) {
					id = i;
					break;
				}
			}
			if (id < 0 && Camera.getNumberOfCameras() > 0) {
				id = 0;
				Camera.getCameraInfo(0, info);
			}
			if (id < 0) {
				onFail(2);
				return;
			}
			cam = Camera.open(id);
			Camera.Parameters p = cam.getParameters();
			Camera.Size best = null;
			for (Camera.Size s : p.getSupportedPreviewSizes()) {
				if (s.width * s.height <= 1280 * 720 && (best == null || s.width * s.height > best.width * best.height)) {
					best = s;
				}
			}
			if (best != null) {
				p.setPreviewSize(best.width, best.height);
			}
			if (p.getSupportedFocusModes().contains(Camera.Parameters.FOCUS_MODE_CONTINUOUS_PICTURE)) {
				p.setFocusMode(Camera.Parameters.FOCUS_MODE_CONTINUOUS_PICTURE);
			}
			cam.setParameters(p);
			final Camera.Size size = cam.getParameters().getPreviewSize();
			final int rot = rotation(a, info);
			// why: a preview needs somewhere to go; the frames reach Go through the callback.
			tex = new SurfaceTexture(0);
			cam.setPreviewTexture(tex);
			cam.setPreviewCallback(new Camera.PreviewCallback() {
				public void onPreviewFrame(byte[] data, Camera c) {
					onFrame(data, size.width, size.height, rot);
				}
			});
			cam.startPreview();
		} catch (Exception e) {
			close();
			onFail(2);
		}
	}

	private static void close() {
		if (cam != null) {
			cam.setPreviewCallback(null);
			cam.stopPreview();
			cam.release();
			cam = null;
		}
		if (tex != null) {
			tex.release();
			tex = null;
		}
	}

	// rotation is how far clockwise a frame turns to stand upright on
	// the screen as it is held now.
	private static int rotation(Activity a, Camera.CameraInfo info) {
		int deg = 0;
		switch (a.getWindowManager().getDefaultDisplay().getRotation()) {
		case Surface.ROTATION_90: deg = 90; break;
		case Surface.ROTATION_180: deg = 180; break;
		case Surface.ROTATION_270: deg = 270; break;
		}
		if (info.facing == Camera.CameraInfo.CAMERA_FACING_FRONT) {
			return (info.orientation + deg) % 360;
		}
		return (info.orientation - deg + 360) % 360;
	}

	private static void hook(Activity a) {
		if (hooked) {
			return;
		}
		hooked = true;
		a.getApplication().registerActivityLifecycleCallbacks(new Application.ActivityLifecycleCallbacks() {
			public void onActivityPaused(Activity x) {
				if (cam != null) {
					close();
					onFail(0);
				}
			}
			public void onActivityCreated(Activity x, Bundle b) {}
			public void onActivityStarted(Activity x) {}
			public void onActivityResumed(Activity x) {}
			public void onActivityStopped(Activity x) {}
			public void onActivitySaveInstanceState(Activity x, Bundle b) {}
			public void onActivityDestroyed(Activity x) {}
		});
	}
}
