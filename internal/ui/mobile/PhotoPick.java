package com.quanticstudios.pitwall;

import android.app.Activity;
import android.app.Fragment;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.View;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;

// A Fragment receives the picker result without changing GioActivity.
@SuppressWarnings("deprecation")
public final class PhotoPick {
    static native void onImage(byte[] data, int error);

    public static void start(final View view, final boolean paste) {
        view.post(new Runnable() { public void run() {
            Activity activity = (Activity) view.getContext();
            if (paste) {
                ClipboardManager cm = (ClipboardManager) activity.getSystemService(Context.CLIPBOARD_SERVICE);
                ClipData clip = cm.getPrimaryClip();
                Uri uri = clip != null && clip.getItemCount() > 0 ? clip.getItemAt(0).getUri() : null;
                if (uri == null) { onImage(null, 2); return; }
                read(activity, uri);
            } else {
                try { activity.getFragmentManager().beginTransaction().add(new Pick(), "pitwall-photo").commit(); }
                catch (RuntimeException e) { onImage(null, 1); }
            }
        }});
    }

    public static final class Pick extends Fragment {
        @Override public void onCreate(Bundle state) {
            super.onCreate(state);
            if (state != null) return;
            Intent intent = new Intent(Build.VERSION.SDK_INT >= 33 ? "android.provider.action.PICK_IMAGES" : Intent.ACTION_GET_CONTENT);
            intent.setType("image/*");
            if (Build.VERSION.SDK_INT < 33) intent.addCategory(Intent.CATEGORY_OPENABLE);
            try { startActivityForResult(intent, 1); }
            catch (RuntimeException e) {
                onImage(null, 1);
                getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
            }
        }

        @Override public void onActivityResult(int request, int result, Intent data) {
            if (result == Activity.RESULT_OK && data != null && data.getData() != null) read(getActivity(), data.getData());
            else onImage(null, 0);
            getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
        }
    }

    private static void read(final Context context, final Uri uri) {
        new Thread(new Runnable() { public void run() {
            try (InputStream in = context.getContentResolver().openInputStream(uri); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
                byte[] buf = new byte[32768]; int n;
                while ((n = in.read(buf)) != -1) {
                    if (out.size() + n > 20 * 1024 * 1024) { onImage(null, 3); return; }
                    out.write(buf, 0, n);
                }
                onImage(out.toByteArray(), 0);
            } catch (Exception e) { onImage(null, 1); }
        }}).start();
    }
}
