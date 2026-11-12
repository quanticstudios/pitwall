package com.quanticstudios.pitwall;

import android.app.Activity;
import android.app.Fragment;
import android.app.PendingIntent;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageInfo;
import android.content.pm.PackageInstaller;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.view.View;
import android.widget.Toast;
import java.io.File;
import java.io.FileInputStream;
import java.io.OutputStream;
import java.lang.ref.WeakReference;

@SuppressWarnings("deprecation")
public final class ApkInstall {
    private static WeakReference<Activity> current = new WeakReference<>(null);
    private static boolean active;
    private static Intent pendingConfirm;
    private static SharedPreferences prefs(Context c) { return c.getSharedPreferences("pitwall-update", Context.MODE_PRIVATE); }

    private static void report(Context c) {
        if (current.get() != null) {
            SharedPreferences p = prefs(c);
            PhotoPick.onInstall(c.getCacheDir().getAbsolutePath(), p.getString("message", ""), p.contains("path"));
            if (!p.contains("path")) p.edit().remove("message").commit();
        }
    }

    private static void finish(Context c, String message, boolean abandon) {
        active = false;
        pendingConfirm = null;
        Activity a = current.get();
        if (a != null) {
            Fragment status = a.getFragmentManager().findFragmentByTag("pitwall-install-status");
            if (status != null) a.getFragmentManager().beginTransaction().remove(status).commitAllowingStateLoss();
        }
        SharedPreferences p = prefs(c);
        int session = p.getInt("session", -1);
        if (abandon && session != -1) {
            try { c.getPackageManager().getPackageInstaller().abandonSession(session); } catch (RuntimeException ignored) {}
        }
        String path = p.getString("path", "");
        if (!path.isEmpty()) new File(path).delete();
        p.edit().remove("path").remove("session").remove("target").putString("message", message).commit();
        report(c);
    }

    private static boolean recover(Activity a) {
        SharedPreferences p = prefs(a);
        String path = p.getString("path", "");
        if (path.isEmpty()) return false;
        long target = p.getLong("target", 0);
        try {
            if (target != 0 && a.getPackageManager().getPackageInfo(a.getPackageName(), 0).getLongVersionCode() >= target) {
                finish(a, "Update installed.", false);
                return true;
            }
        } catch (Exception ignored) {}
        int id = p.getInt("session", -1);
        PackageInstaller.SessionInfo session = id < 0 ? null : a.getPackageManager().getPackageInstaller().getSessionInfo(id);
        if (!active || !new File(path).isFile() || (id >= 0 && session == null)) {
            finish(a, "", true);
            return true;
        }
        return false;
    }

    public static void start(final View view, final String path) {
        view.post(new Runnable() { public void run() {
            Activity a = (Activity) view.getContext();
            current = new WeakReference<>(a);
            boolean recovered = recover(a);
            if (path.isEmpty()) {
                if (!recovered) report(a);
                return;
            }
            File apk = new File(path);
            try {
                if (!apk.getCanonicalFile().getParentFile().equals(a.getCacheDir().getCanonicalFile())) throw new Exception("APK is outside the app cache");
                if (prefs(a).contains("path")) {
                    if (!path.equals(prefs(a).getString("path", ""))) apk.delete();
                    report(a);
                    return;
                }
                PackageInfo candidate = a.getPackageManager().getPackageArchiveInfo(path, 0);
                PackageInfo installed = a.getPackageManager().getPackageInfo(a.getPackageName(), 0);
                if (candidate == null || !a.getPackageName().equals(candidate.packageName)) throw new Exception("APK is not a pitwall update");
                if (candidate.getLongVersionCode() <= installed.getLongVersionCode()) throw new Exception("APK is not newer than this build");
                prefs(a).edit().putString("path", path).putLong("target", candidate.getLongVersionCode()).putString("message", "Preparing installation…").commit();
                active = true;
                if (a.getFragmentManager().findFragmentByTag("pitwall-install-status") == null) {
                    a.getFragmentManager().beginTransaction().add(new Status(), "pitwall-install-status").commitAllowingStateLoss();
                }
                if (!a.getPackageManager().canRequestPackageInstalls()) {
                    String explanation = "Allow pitwall to install updates, then return.";
                    prefs(a).edit().putString("message", explanation).commit();
                    report(a);
                    Toast.makeText(a, explanation, Toast.LENGTH_LONG).show();
                    a.getFragmentManager().beginTransaction().add(new Permission(), "pitwall-install").commitAllowingStateLoss();
                } else begin(a);
            } catch (Exception e) {
                apk.delete();
                finish(a, "Installation failed: " + e.getMessage(), true);
            }
        }});
    }

    public static final class Permission extends Fragment {
        private boolean started;
        @Override public void onCreate(Bundle state) {
            super.onCreate(state);
            started = state != null && state.getBoolean("started");
        }
        @Override public void onSaveInstanceState(Bundle state) {
            super.onSaveInstanceState(state);
            state.putBoolean("started", started);
        }
        @Override public void onResume() {
            super.onResume();
            if (!prefs(getActivity()).contains("path")) {
                getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
                return;
            }
            if (started) return;
            started = true;
            try { startActivityForResult(new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:" + getActivity().getPackageName())), 1); }
            catch (RuntimeException e) { finish(getActivity(), "Could not open install settings: " + e.getMessage(), true); }
        }
        @Override public void onActivityResult(int request, int result, Intent data) {
            Activity a = getActivity();
            if (a.getPackageManager().canRequestPackageInstalls()) begin(a);
            else finish(a, "Installation cancelled. Allow pitwall to install updates to continue.", true);
            getFragmentManager().beginTransaction().remove(this).commitAllowingStateLoss();
        }
    }

    public static final class Status extends Fragment {
        @Override public void onResume() {
            super.onResume();
            showConfirmation(getActivity());
        }
    }

    private static void showConfirmation(Activity a) {
        Fragment status = a.getFragmentManager().findFragmentByTag("pitwall-install-status");
        if (pendingConfirm == null || status == null || !status.isResumed()) return;
        Intent confirm = pendingConfirm;
        pendingConfirm = null;
        // A dialog left open across a process restart still holds its old session.
        try { a.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TASK)); }
        catch (RuntimeException e) { finish(a, "Could not show installation confirmation: " + e.getMessage(), true); }
    }

    private static void begin(final Activity a) {
        prefs(a).edit().putString("message", "Preparing installation…").commit();
        report(a);
        new Thread(new Runnable() { public void run() {
            PackageInstaller installer = a.getPackageManager().getPackageInstaller();
            try {
                File apk = new File(prefs(a).getString("path", ""));
                PackageInstaller.SessionParams params = new PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL);
                params.setAppPackageName(a.getPackageName());
                params.setSize(apk.length());
                if (Build.VERSION.SDK_INT >= 31) params.setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_REQUIRED);
                int id = installer.createSession(params);
                prefs(a).edit().putInt("session", id).commit();
                try (PackageInstaller.Session session = installer.openSession(id)) {
                    try (FileInputStream in = new FileInputStream(apk); OutputStream out = session.openWrite("pitwall-phone.apk", 0, apk.length())) {
                        byte[] buffer = new byte[65536];
                        int n;
                        while ((n = in.read(buffer)) != -1) out.write(buffer, 0, n);
                        session.fsync(out);
                    }
                    Intent result = new Intent(a, Result.class).setAction(a.getPackageName() + ".INSTALL_RESULT");
                    int flags = PendingIntent.FLAG_UPDATE_CURRENT;
                    if (Build.VERSION.SDK_INT >= 31) flags |= PendingIntent.FLAG_MUTABLE;
                    session.commit(PendingIntent.getBroadcast(a, id, result, flags).getIntentSender());
                }
            } catch (final Exception e) {
                a.runOnUiThread(new Runnable() { public void run() { finish(a, "Installation failed: " + e.getMessage(), true); }});
            }
        }}, "pitwall-install").start();
    }

    public static final class Result extends BroadcastReceiver {
        @Override public void onReceive(Context c, Intent intent) {
            if (!(c.getPackageName() + ".INSTALL_RESULT").equals(intent.getAction())) return;
            if (intent.getIntExtra(PackageInstaller.EXTRA_SESSION_ID, -1) != prefs(c).getInt("session", -2)) return;
            int status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE);
            if (status == PackageInstaller.STATUS_PENDING_USER_ACTION) {
                prefs(c).edit().putString("message", "Confirm installation in Android.").commit();
                report(c);
                pendingConfirm = intent.getParcelableExtra(Intent.EXTRA_INTENT);
                Activity a = current.get();
                if (a == null) finish(c, "Return to pitwall and tap Install again.", true);
                else showConfirmation(a);
                return;
            }
            String message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE);
            if (status == PackageInstaller.STATUS_SUCCESS) finish(c, "Update installed.", false);
            else if (status == PackageInstaller.STATUS_FAILURE_ABORTED) finish(c, "Installation cancelled.", true);
            else finish(c, "Installation failed: " + (message == null ? "Android status " + status : message), true);
        }
    }
}
