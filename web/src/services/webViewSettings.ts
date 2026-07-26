export const DEFAULT_WEBVIEW_TEXT_ZOOM = 100;
export const MIN_WEBVIEW_TEXT_ZOOM = 50;
export const MAX_WEBVIEW_TEXT_ZOOM = 200;

type WebViewSettingsWindow = Window & {
  MindFSWebViewSettings?: {
    getTextZoom?: () => number | string;
    setTextZoom?: (value: number) => void;
  };
};

function clampTextZoom(value: number): number {
  if (!Number.isFinite(value)) {
    return DEFAULT_WEBVIEW_TEXT_ZOOM;
  }
  return Math.min(MAX_WEBVIEW_TEXT_ZOOM, Math.max(MIN_WEBVIEW_TEXT_ZOOM, Math.round(value)));
}

function getBridge(): WebViewSettingsWindow["MindFSWebViewSettings"] {
  if (typeof window === "undefined") {
    return undefined;
  }
  return (window as WebViewSettingsWindow).MindFSWebViewSettings;
}

export function hasNativeWebViewSettings(): boolean {
  const bridge = getBridge();
  return (
    typeof bridge?.getTextZoom === "function" &&
    typeof bridge?.setTextZoom === "function"
  );
}

export function getNativeWebViewTextZoom(): number {
  const bridge = getBridge();
  if (typeof bridge?.getTextZoom !== "function") {
    return DEFAULT_WEBVIEW_TEXT_ZOOM;
  }
  return clampTextZoom(Number(bridge.getTextZoom()));
}

export function setNativeWebViewTextZoom(value: number): number {
  const nextValue = clampTextZoom(value);
  const bridge = getBridge();
  if (typeof bridge?.setTextZoom === "function") {
    bridge.setTextZoom(nextValue);
  }
  return nextValue;
}
