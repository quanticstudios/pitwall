package com.quanticstudios.pitwall;

import android.app.Activity;
import android.app.Fragment;
import android.content.ClipData;
import android.content.ClipDescription;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.ContentInfo;
import android.view.OnReceiveContentListener;
import android.view.View;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;

// A Fragment receives the picker result without changing GioActivity.
@SuppressWarnings("deprecation")
public final class PhotoPick {
    static native void onImage(byte[] data, String type, int error);
    static native void onInsert(byte[] data, String type, int error);
    // onClip gets the clipboard as lines of "kind encoded-value" and returns the URI to read,
    // or null after it has reported what the clipboard holds.
    static native String onClip(String clip);

    // mode: 0 opens the photo picker, 1 reads the clipboard, 2 accepts keyboard images.
    public static void start(final View view, final int mode) {
        view.post(new Runnable() { public void run() {
            Activity activity = (Activity) view.getContext();
            if (mode == 2) {
                if (Build.VERSION.SDK_INT >= 31) view.setOnReceiveContentListener(new String[]{"image/*"}, new Insert());
            } else if (mode == 1) {
                ClipboardManager cm = (ClipboardManager) activity.getSystemService(Context.CLIPBOARD_SERVICE);
                String uri;
                try { uri = onClip(describe(cm.getPrimaryClip())); }
                catch (RuntimeException e) { onImage(null, null, 1); return; }
                if (uri != null) read(activity, Uri.parse(uri), false);
            } else {
                try { activity.getFragmentManager().beginTransaction().add(new Pick(), "pitwall-photo").commit(); }
                catch (RuntimeException e) { onImage(null, null, 1); }
            }
        }});
    }

    // describe writes the clip's MIME types and each item's URI, text and HTML, URI-encoded so a value is one line.
    private static String describe(ClipData clip) {
        StringBuilder b = new StringBuilder();
        if (clip == null) return "";
        ClipDescription d = clip.getDescription();
        for (int i = 0; d != null && i < d.getMimeTypeCount(); i++) line(b, "mime", d.getMimeType(i));
        for (int i = 0; i < clip.getItemCount(); i++) {
            ClipData.Item it = clip.getItemAt(i);
            line(b, "item", "");
            if (it.getUri() != null) line(b, "uri", it.getUri().toString());
            if (it.getText() != null) line(b, "text", it.getText().toString());
            if (it.getHtmlText() != null) line(b, "html", it.getHtmlText());
        }
        return b.toString();
    }

    private static void line(StringBuilder b, String kind, String v) {
        // A URI list or an <img> tag fits in 64K characters; a long text needs only its kind.
        if (v.length() > 65536) v = v.substring(0, 65536);
        b.append(kind).append(' ').append(Uri.encode(v)).append('\n');
    }

    public static final class Insert implements OnReceiveContentListener {
        @Override public ContentInfo onReceiveContent(View view, ContentInfo payload) {
            ClipData clip = payload.getClip();
            for (int i = 0; i < clip.getItemCount(); i++) {
                Uri uri = clip.getItemAt(i).getUri();
                if (uri != null) { read(view.getContext(), uri, true); return null; }
            }
            return payload;
        }
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
                onImage(null, null, 1);
                getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
            }
        }

        @Override public void onActivityResult(int request, int result, Intent data) {
            if (result == Activity.RESULT_OK && data != null && data.getData() != null) read(getActivity(), data.getData(), false);
            else onImage(null, null, 0);
            getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
        }
    }

    private static void done(boolean insert, byte[] data, String type, int error) {
        if (insert) onInsert(data, type, error);
        else onImage(data, type, error);
    }

    private static void read(final Context context, final Uri uri, final boolean insert) {
        new Thread(new Runnable() { public void run() {
            String type = null;
            try { type = context.getContentResolver().getType(uri); } catch (Exception e) {}
            try (InputStream in = context.getContentResolver().openInputStream(uri); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
                byte[] buf = new byte[32768]; int n;
                while ((n = in.read(buf)) != -1) {
                    if (out.size() + n > 20 * 1024 * 1024) { done(insert, null, type, 3); return; }
                    out.write(buf, 0, n);
                }
                done(insert, out.toByteArray(), type, 0);
            } catch (SecurityException | java.io.FileNotFoundException e) { done(insert, null, type, 4); }
            catch (Exception e) { done(insert, null, type, 1); }
        }}).start();
    }
}
