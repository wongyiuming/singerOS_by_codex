package com.n1s.receiver;

import android.app.Activity;
import android.content.Intent;
import android.graphics.Typeface;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.Settings;
import android.view.Gravity;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.net.Inet4Address;
import java.net.NetworkInterface;
import java.util.Collections;

public class MainActivity extends Activity {
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        startReceiverService();

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(48, 48, 48, 48);
        root.setGravity(Gravity.CENTER_VERTICAL);

        TextView title = new TextView(this);
        title.setText("N1S Receiver");
        title.setTextSize(30);
        title.setTypeface(Typeface.DEFAULT_BOLD);
        root.addView(title, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        String ip = findIpv4();
        TextView info = new TextView(this);
        info.setTextSize(18);
        info.setTypeface(Typeface.MONOSPACE);
        info.setPadding(0, 28, 0, 28);
        info.setText(
                "监听: 0.0.0.0:9876\n" +
                "本机: " + ip + ":9876\n\n" +
                "PC 上传 APK:\n" +
                "curl -T K7.apk http://" + ip + ":9876/upload/K7.apk\n\n" +
                "上传完成后会自动拉起系统 APK 安装器。\n" +
                "Receiver 会以前台服务运行，并在开机后自动启动。"
        );
        root.addView(info, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !getPackageManager().canRequestPackageInstalls()) {
            Button allow = new Button(this);
            allow.setText("允许 Receiver 安装 APK");
            allow.setFocusable(true);
            allow.setOnClickListener(v -> {
                Intent intent = new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                        Uri.parse("package:" + getPackageName()));
                startActivity(intent);
            });
            root.addView(allow, new LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        }

        setContentView(root);
    }

    private void startReceiverService() {
        Intent intent = new Intent(this, ReceiverService.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(intent);
        } else {
            startService(intent);
        }
    }

    private String findIpv4() {
        try {
            for (NetworkInterface ni : Collections.list(NetworkInterface.getNetworkInterfaces())) {
                if (!ni.isUp() || ni.isLoopback()) continue;
                for (java.net.InetAddress address : Collections.list(ni.getInetAddresses())) {
                    if (address instanceof Inet4Address && !address.isLoopbackAddress()) {
                        return address.getHostAddress();
                    }
                }
            }
        } catch (Exception ignored) {
        }
        return "N1S_IP";
    }
}
