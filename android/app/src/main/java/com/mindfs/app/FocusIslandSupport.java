package com.mindfs.app;

import android.app.Notification;
import android.app.PendingIntent;
import android.content.Context;
import android.graphics.Bitmap;
import android.graphics.Canvas;
import android.graphics.Path;
import android.graphics.drawable.Drawable;
import android.graphics.drawable.Icon;
import android.net.Uri;
import android.os.Bundle;
import android.provider.Settings;
import android.util.Log;
import android.view.View;
import android.widget.RemoteViews;
import java.util.Locale;
import org.json.JSONObject;

/**
 * 小米 HyperOS 超级岛 / 焦点通知支持（fork 自研）。
 *
 * 往已构建的通知 extras 附加 miui.focus.param（param_v2 JSON）与 miui.focus.pics 图标包：
 * OS3 设备显示超级岛 + 焦点通知，OS2 设备显示焦点通知 + 状态栏 ticker，
 * 其余设备忽略这些 extras、按普通通知显示，因此对非小米设备零影响。
 *
 * 仅当运行时探测到 notification_focus_protocol >= 2 且本应用焦点通知权限
 * （canShowFocus）已开启时才附加参数；探测结果由 refresh() 缓存。
 */
final class FocusIslandSupport {
    private static final String TAG = "MindFSFocusIsland";
    private static final String PIC_APP = "miui.focus.pic_mindfs";
    private static final String PIC_AGENT = "miui.focus.pic_agent";

    /**
     * 外圈光效开关。SystemUI 的 DynamicIslandUtils.isGlowEffectEnabledForExpandState 只判断
     * miui.effect.src 非空，值本身不被解析为资源；社区约定填 outer_glow。
     *
     * 大岛的光走另一个 key（miui.bigIsland.effect.src），而 FocusNotificationController 转交
     * 岛数据时是逐键白名单拷贝、并不包含它，普通应用写了也到不了 DynamicIslandData，
     * 因此这里只做展开态的光。
     */
    private static final String EFFECT_SRC = "outer_glow";
    /** 光色，经通知 extras 的 miui.effect.color 直达岛数据。 */
    private static final String EFFECT_COLOR = "#10A37F";

    private static volatile int protocolVersion = 0;
    private static volatile boolean focusPermission = false;
    private static volatile Icon appIcon;

    private FocusIslandSupport() {}

    /** 探测焦点通知协议版本与权限。canShowFocus 是耗时的 provider 调用，须在后台线程执行。 */
    static void refresh(Context context) {
        int protocol = 0;
        boolean permission = false;
        try {
            protocol = Settings.System.getInt(
                context.getContentResolver(), "notification_focus_protocol", 0);
            if (protocol >= 2) {
                permission = canShowFocus(context);
            }
        } catch (Exception ex) {
            Log.w(TAG, "detect focus support failed", ex);
        }
        protocolVersion = protocol;
        focusPermission = permission;
        Log.i(TAG, "focus protocol=" + protocol + " permission=" + permission);
    }

    static boolean isEnabled() {
        return protocolVersion >= 2 && focusPermission;
    }

    private static boolean canShowFocus(Context context) {
        try {
            Uri uri = Uri.parse("content://miui.statusbar.notification.public");
            Bundle extras = new Bundle();
            extras.putString("package", context.getPackageName());
            Bundle result = context.getContentResolver().call(uri, "canShowFocus", null, extras);
            return result != null && result.getBoolean("canShowFocus", false);
        } catch (Exception ex) {
            return false;
        }
    }

    /**
     * 给已构建的通知附加岛参数（build 后 extras 仍是同一个 Bundle，可直接写入）。
     *
     * 大岛：左侧圆形应用图标 + 项目名，右侧「回复中… / 已完成」，外圈带光效。
     * 小岛：仅应用图标。
     * 展开态：由 {@link #buildExpandView} 提供的自定义 RemoteViews 接管。
     *
     * @param sessionTitle 会话标题（展开态主标题）
     * @param projectTitle 项目名（大岛主文本、展开态副标题）
     * @param chatTitle    焦点通知与 ticker / 息屏的标题
     * @param chatContent  回复摘要，同时用作展开态摘要区文案，可为空
     * @param chronometerBase 展开态秒表基准（SystemClock.elapsedRealtime 时基），回复中有效
     * @param elapsedText  完成态定格耗时文案，回复中传 null
     * @param openIntent   展开态「打开会话」按钮的跳转，可为空
     */
    static void attach(Context context, Notification notification, boolean replying,
                       String sessionTitle, String projectTitle, String chatTitle, String chatContent,
                       long chronometerBase, String elapsedText, PendingIntent openIntent) {
        attach(context, notification, replying,
            sessionTitle, projectTitle, chatTitle, chatContent,
            chronometerBase, elapsedText, openIntent, "");
    }

    /**
     * 附加岛参数。agentName 为当前回复驱动的 agent（如 "claude" / "codex"），
     * 岛图标按 agent 名取对应图标（{@link #agentIcon}），未知 agent 回退应用图标。
     */
    static void attach(Context context, Notification notification, boolean replying,
                       String sessionTitle, String projectTitle, String chatTitle, String chatContent,
                       long chronometerBase, String elapsedText, PendingIntent openIntent,
                       String agentName) {
        attach(context, notification, replying, false,
            sessionTitle, projectTitle, chatTitle, chatContent,
            chronometerBase, elapsedText, openIntent, agentName);
    }

    /**
     * 附加岛参数。agentName 为当前回复驱动的 agent（如 "claude" / "codex"），
     * 岛图标按 agent 名取对应图标（{@link #agentIcon}），未知 agent 回退应用图标。
     * askUserWaiting 为 true 表示该会话正停在 agent 提问等待用户回答，岛状态文案
     * 显示「需要你输入」而不是「回复中…」。
     */
    static void attach(Context context, Notification notification, boolean replying, boolean askUserWaiting,
                       String sessionTitle, String projectTitle, String chatTitle, String chatContent,
                       long chronometerBase, String elapsedText, PendingIntent openIntent,
                       String agentName) {
        attach(context, notification, replying, askUserWaiting, "",
            sessionTitle, projectTitle, chatTitle, chatContent,
            chronometerBase, elapsedText, openIntent, agentName);
    }

    /**
     * 附加岛参数。askUserQuestion 为等待输入时后端透出的问题+选项文本，展开态的
     * 摘要区会优先显示它而不是笼统的「等待你的回答…」。
     */
    static void attach(Context context, Notification notification, boolean replying, boolean askUserWaiting,
                       String askUserQuestion,
                       String sessionTitle, String projectTitle, String chatTitle, String chatContent,
                       long chronometerBase, String elapsedText, PendingIntent openIntent,
                       String agentName) {
        if (!isEnabled() || notification == null) {
            return;
        }
        try {
            Bundle pics = new Bundle();
            pics.putParcelable(PIC_APP, roundAppIcon(context));
            // 当前 agent 的图标：走模板 picInfo 通道（非 RemoteViews），让摘要态大岛/小岛
            // 显示对应 agent 图标。SystemUI 对动画 WebP 只取静态首帧，故这里用静态 PNG。
            Icon agentIcon = agentIcon(context, agentName);
            pics.putParcelable(PIC_AGENT, agentIcon);
            notification.extras.putBundle("miui.focus.pics", pics);

            // 自定义 RemoteViews 模式。SystemUI 的 onNotificationPosted 以 extras 里有没有
            // miui.focus.rv 硬分叉：有则走 buildNoParamsFocusNotification（读 param.custom），
            // 无则走模板（读 param）。唯一渲染展开态自定义视图的 createCustomView 只在前一条
            // 分支里被调用，所以 miui.focus.rv 必须存在，且参数键要换成 param.custom。
            notification.extras.putString("miui.focus.param.custom",
                buildParams(replying, askUserWaiting, sessionTitle, projectTitle, chatTitle, chatContent, agentName));
            // 光色。自定义模式的 fillCustomViewNotifiParams 只搬 outEffectSrc，不读
            // outEffectColor；而 FocusNotificationController 转交岛数据时会直接从通知
            // extras 取 miui.effect.color，所以写在这里才生效。
            notification.extras.putString("miui.effect.color", EFFECT_COLOR);
            // 展开态光效开关。fillCustomViewNotifiParams 会用根级 outEffectSrc 覆写同一个 key，
            // 这里显式写一遍与实机抓包的生效样本保持一致，也兜住那条搬运路径的差异。
            notification.extras.putString("miui.effect.src", EFFECT_SRC);
            // 大岛光效开关。isGlowEffectEnabledForBigState 只判非空，但该 key 不在
            // FocusNotificationController 的白名单拷贝里，普通应用能否送达岛数据待实测；
            // 生效样本里有，写上不产生副作用。
            notification.extras.putString("miui.bigIsland.effect.src", EFFECT_SRC);

            // 通知栏焦点卡片（明/暗）。createCustomView 在 miui.focus.rv 为 null 时会提前
            // return，连带跳过 tiny / deco，所以这张即使用不上也必须给。
            notification.extras.putParcelable("miui.focus.rv",
                buildCard(context, replying, askUserWaiting, askUserQuestion, sessionTitle, projectTitle, chatContent,
                    chronometerBase, elapsedText, openIntent, false, agentName));
            notification.extras.putParcelable("miui.focus.rvNight",
                buildCard(context, replying, askUserWaiting, askUserQuestion, sessionTitle, projectTitle, chatContent,
                    chronometerBase, elapsedText, openIntent, true, agentName));
            // 岛展开态：点击岛后悬浮的卡片，恒为深色背景
            notification.extras.putParcelable("miui.focus.rv.island.expand",
                buildCard(context, replying, askUserWaiting, askUserQuestion, sessionTitle, projectTitle, chatContent,
                    chronometerBase, elapsedText, openIntent, true, agentName));
        } catch (Exception ex) {
            Log.w(TAG, "attach island params failed", ex);
        }
    }

    /**
     * 焦点卡片 / 岛展开态共用布局：头部（图标 + 会话名 / 项目名 + 状态标签）、摘要区、
     * 底部（耗时 + 打开会话）。dark=true 用于岛展开态与通知栏深色模式。
     */
    private static RemoteViews buildCard(Context context, boolean replying, boolean askUserWaiting,
                                         String askUserQuestion, String sessionTitle, String projectTitle,
                                         String summary, long chronometerBase, String elapsedText,
                                         PendingIntent openIntent, boolean dark, String agentName) {
        RemoteViews views = new RemoteViews(context.getPackageName(), R.layout.island_expand);
        views.setImageViewIcon(R.id.island_icon, agentIcon(context, agentName));

        views.setTextViewText(R.id.island_title,
            sessionTitle == null || sessionTitle.isEmpty() ? "会话" : sessionTitle);
        views.setTextColor(R.id.island_title, dark ? 0xFFFFFFFF : 0xFF1A1A1A);
        views.setTextViewText(R.id.island_subtitle, projectTitle == null ? "" : projectTitle);
        views.setTextColor(R.id.island_subtitle, dark ? 0xFF93989E : 0xFF6B7075);

        views.setTextViewText(R.id.island_chip, chipText(replying, askUserWaiting));
        if (replying) {
            views.setInt(R.id.island_chip, "setBackgroundResource",
                dark ? R.drawable.island_expand_chip_replying
                     : R.drawable.island_expand_chip_replying_light);
            views.setTextColor(R.id.island_chip, dark ? 0xFFD7DADE : 0xFF4A4F55);
        } else {
            views.setInt(R.id.island_chip, "setBackgroundResource",
                dark ? R.drawable.island_expand_chip_done
                     : R.drawable.island_expand_chip_done_light);
            views.setTextColor(R.id.island_chip, dark ? 0xFF34C759 : 0xFF0F9F7C);
        }

        String summaryText = summary;
        if (replying && askUserWaiting) {
            // 等待输入：优先显示后端透出的问题+选项，没有才用占位
            if (askUserQuestion != null && !askUserQuestion.isEmpty()) {
                summaryText = askUserQuestion;
            } else if (summary == null || summary.isEmpty()) {
                summaryText = summaryPlaceholder(replying, true);
            }
        } else if (summary == null || summary.isEmpty()) {
            summaryText = summaryPlaceholder(replying, false);
        }
        views.setTextViewText(R.id.island_summary, summaryText);
        views.setInt(R.id.island_summary, "setBackgroundResource",
            dark ? R.drawable.island_expand_summary_bg : R.drawable.island_expand_summary_bg_light);
        views.setTextColor(R.id.island_summary, dark ? 0xFFB6BCC2 : 0xFF4A4F55);

        if (replying) {
            // 秒表由系统逐秒自走，两次轮询之间也在走，不必为计时重发通知
            views.setViewVisibility(R.id.island_timer, View.VISIBLE);
            views.setViewVisibility(R.id.island_elapsed, View.GONE);
            views.setChronometer(R.id.island_timer, chronometerBase, null, true);
            views.setTextColor(R.id.island_timer, dark ? 0xFFFFFFFF : 0xFF1A1A1A);
            views.setTextViewText(R.id.island_timer_label, "已运行");
        } else {
            boolean hasElapsed = elapsedText != null && !elapsedText.isEmpty();
            views.setViewVisibility(R.id.island_timer, View.GONE);
            views.setViewVisibility(R.id.island_elapsed, hasElapsed ? View.VISIBLE : View.GONE);
            views.setTextViewText(R.id.island_elapsed, hasElapsed ? elapsedText : "");
            views.setTextColor(R.id.island_elapsed, dark ? 0xFF34C759 : 0xFF0F9F7C);
            views.setTextViewText(R.id.island_timer_label, hasElapsed ? "耗时" : "");
        }
        views.setTextColor(R.id.island_timer_label, dark ? 0xFF8A9096 : 0xFF8A9096);

        views.setTextViewText(R.id.island_open, "打开会话");
        if (openIntent != null) {
            views.setOnClickPendingIntent(R.id.island_open, openIntent);
        }
        return views;
    }

    /** 焦点卡片状态文案：等待用户输入时显示「需要你输入」。 */
    private static String chipText(boolean replying, boolean askUserWaiting) {
        if (replying && askUserWaiting) {
            return "需要你输入";
        }
        return replying ? "回复中" : "已完成";
    }

    /** 摘要占位文案：等待用户输入时提示用户作答，否则沿用回复中/完成占位。 */
    private static String summaryPlaceholder(boolean replying, boolean askUserWaiting) {
        if (replying && askUserWaiting) {
            return "等待你的回答…";
        }
        return replying ? "正在生成回复…" : "回复已完成";
    }

    private static String buildParams(boolean replying, boolean askUserWaiting,
                                      String sessionTitle, String projectTitle,
                                      String chatTitle, String chatContent, String agentName) throws Exception {
        String statusText = replying ? (askUserWaiting ? "需要你输入" : "回复中…") : "已完成";
        // 胶囊横向空间有限，只放项目名一行；会话标题交给展开态
        String islandPrimary = projectTitle != null && !projectTitle.isEmpty()
            ? projectTitle
            : (sessionTitle == null || sessionTitle.isEmpty() ? "会话" : sessionTitle);
        JSONObject aTextInfo = new JSONObject().put("title", islandPrimary);
        // agent 名非空时摘要态大岛/小岛用对应 agent 图标，否则应用图标
        JSONObject agentRef = hasAgentIcon(agentName) ? agentPicRef() : picRef();
        JSONObject bigIslandArea = new JSONObject()
            .put("imageTextInfoLeft", new JSONObject()
                .put("type", 1)
                .put("picInfo", agentRef)
                .put("textInfo", aTextInfo))
            .put("textInfo", new JSONObject()
                .put("title", statusText)
                .put("showHighlightColor", !replying || askUserWaiting));
        JSONObject smallIslandArea = new JSONObject()
            .put("picInfo", agentRef);

        JSONObject paramIsland = new JSONObject()
            .put("islandProperty", 1)
            .put("islandTimeout", replying ? 4 * 3600 : 900)
            // 岛级光效开关。与根级 outEffectSrc 是两个不同位置：根级那个被
            // fillCustomViewNotifiParams 搬去 miui.effect.src（展开态），这个岛级的才是
            // 大岛自己的光——实机抓包的生效样本里两处都有。
            .put("outEffectSrc", EFFECT_SRC)
            // showHighlightColor 需要有强调色可用，否则完成态的高亮会落到系统默认色
            .put("highlightColor", EFFECT_COLOR)
            .put("bigIslandArea", bigIslandArea)
            .put("smallIslandArea", smallIslandArea);

        // param.custom 是扁平结构：fillCustomViewNotifiParams / handleAodAndStatusBar 直接从
        // 根级读 timeout / enableFloat / ticker / outEffectSrc，不像模板那样解包 param_v2。
        // 不放 chatInfo —— 自定义模式下通知卡片由 miui.focus.rv 提供，模板模块不会被渲染。
        return new JSONObject()
            .put("business", "mindfs_reply")
            .put("isShowNotification", true)
            .put("updatable", true)
            .put("reopen", "reopen")
            .put("timeout", 720)
            // 回复中每 5s 轮询更新一次，自动展开会变成骚扰；完成与等待输入的那次更新要展开提示
            .put("islandFirstFloat", !replying || askUserWaiting)
            .put("enableFloat", !replying || askUserWaiting)
            // 展开态外圈光效开关，会被搬到 extras 的 miui.effect.src；只判非空。
            // 注意自定义模式不读 outEffectColor，光色须由 attach 直接写 miui.effect.color。
            .put("outEffectSrc", EFFECT_SRC)
            .put("ticker", chatTitle)
            .put("tickerPic", PIC_APP)
            .put("aodTitle", chatTitle)
            .put("aodPic", PIC_APP)
            .put("param_island", paramIsland)
            .toString();
    }

    private static JSONObject picRef() throws Exception {
        return new JSONObject().put("type", 1).put("pic", PIC_APP);
    }

    /** agent 图标 pics 引用：摘要态大岛/小岛用对应 agent 的图标。 */
    private static JSONObject agentPicRef() throws Exception {
        return new JSONObject().put("type", 1).put("pic", PIC_AGENT);
    }

    /**
     * agent 名 → drawable 资源映射。文件名与 web/public/assets/agents/ 下的图标对应。
     * 匹配逻辑与前端 AgentIcon 保持一致（精确名 + 常见别名）。
     */
    private static int agentDrawableId(String agentName) {
        if (agentName == null) {
            return 0;
        }
        String n = agentName.trim().toLowerCase(Locale.US);
        if (n.isEmpty()) {
            return 0;
        }
        if (n.equals("claude") || n.equals("claudecode") || n.equals("claude-code")) {
            return R.drawable.agent_claude;
        }
        if (n.equals("codex")) {
            return R.drawable.agent_codex;
        }
        if (n.equals("augment")) {
            return R.drawable.agent_augment;
        }
        if (n.equals("cline")) {
            return R.drawable.agent_cline;
        }
        if (n.equals("copilot")) {
            return R.drawable.agent_copilot;
        }
        if (n.equals("cursor")) {
            return R.drawable.agent_cursor;
        }
        if (n.equals("gemini")) {
            return R.drawable.agent_gemini;
        }
        if (n.equals("grok")) {
            return R.drawable.agent_grok;
        }
        if (n.equals("hermes")) {
            return R.drawable.agent_hermes;
        }
        if (n.equals("kimi")) {
            return R.drawable.agent_kimi;
        }
        if (n.equals("kiro")) {
            return R.drawable.agent_kiro;
        }
        if (n.equals("omp") || n.equals("oh-my-pi")) {
            return R.drawable.agent_omp;
        }
        if (n.equals("openclaw")) {
            return R.drawable.agent_openclaw;
        }
        if (n.equals("opencode")) {
            return R.drawable.agent_opencode;
        }
        if (n.equals("pi")) {
            return R.drawable.agent_pi;
        }
        if (n.equals("qoder")) {
            return R.drawable.agent_qoder;
        }
        if (n.equals("qwen")) {
            return R.drawable.agent_qwen;
        }
        if (n.equals("reasonix")) {
            return R.drawable.agent_reasonix;
        }
        return 0;
    }

    /** 该 agent 是否有对应的图标资源（无则用应用图标兜底）。 */
    private static boolean hasAgentIcon(String agentName) {
        return agentDrawableId(agentName) != 0;
    }

    /** 按 agent 名取岛图标：命中则用 agent 图标，否则应用图标兜底。 */
    private static Icon agentIcon(Context context, String agentName) {
        int resId = agentDrawableId(agentName);
        if (resId != 0) {
            try {
                return Icon.createWithResource(context, resId);
            } catch (Exception ex) {
                Log.w(TAG, "load agent icon failed, fallback to app icon: " + agentName, ex);
            }
        }
        return roundAppIcon(context);
    }

    /**
     * launcher 图标在 API 26+ 解析为自适应图标 XML，直接塞给 SystemUI 会渲染成方形，
     * 这里自行画成圆形位图。
     */
    private static Icon roundAppIcon(Context context) {
        Icon cached = appIcon;
        if (cached != null) {
            return cached;
        }
        try {
            Drawable drawable = context.getPackageManager().getApplicationIcon(context.getPackageName());
            int size = Math.max(48, Math.round(48 * context.getResources().getDisplayMetrics().density));
            Bitmap bitmap = Bitmap.createBitmap(size, size, Bitmap.Config.ARGB_8888);
            Canvas canvas = new Canvas(bitmap);
            Path clip = new Path();
            clip.addOval(0f, 0f, size, size, Path.Direction.CW);
            canvas.clipPath(clip);
            drawable.setBounds(0, 0, size, size);
            drawable.draw(canvas);
            cached = Icon.createWithBitmap(bitmap);
        } catch (Exception ex) {
            cached = Icon.createWithResource(context, R.mipmap.ic_launcher);
        }
        appIcon = cached;
        return cached;
    }
}
