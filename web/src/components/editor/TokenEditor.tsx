import React, { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { LexicalComposer } from "@lexical/react/LexicalComposer";
import { PlainTextPlugin } from "@lexical/react/LexicalPlainTextPlugin";
import { ContentEditable } from "@lexical/react/LexicalContentEditable";
import { HistoryPlugin } from "@lexical/react/LexicalHistoryPlugin";
import { useLexicalComposerContext } from "@lexical/react/LexicalComposerContext";
import {
  $createTextNode,
  $getNearestNodeFromDOMNode,
  $getRoot,
  $getSelection,
  $isLineBreakNode,
  $isRangeSelection,
  $isTextNode,
  $setCompositionKey,
  COMMAND_PRIORITY_HIGH,
  KEY_BACKSPACE_COMMAND,
  KEY_DELETE_COMMAND,
  EditorConfig,
  EditorState,
  KEY_ENTER_COMMAND,
  LexicalEditor,
  NodeKey,
  PASTE_COMMAND,
  SerializedTextNode,
  Spread,
  TextNode,
} from "lexical";

type TokenType = "file" | "skill";
type CandidateType = TokenType | "slash_command" | "prompt" | "command";
type ActiveTokenType = "file" | "slash" | "prompt" | "command";

type ActiveToken = {
  type: ActiveTokenType;
  query: string;
};

export type TokenEditorHandle = {
  focus: () => void;
  blur: () => void;
  getHeight: () => number;
  clear: () => void;
  setText: (value: string) => void;
  insertCandidate: (type: CandidateType, value: string) => void;
};

type TokenEditorProps = {
  placeholder: string;
  disabled?: boolean;
  isDark?: boolean;
  rightInset?: number;
  topInset?: number;
  bottomInset?: number;
  fillHeight?: boolean;
  onChange: (payload: { serializedText: string; displayText: string; activeToken: ActiveToken | null }) => void;
  onFocusChange?: (focused: boolean) => void;
  onPointerDown?: () => void;
  onKeyDown?: (event: React.KeyboardEvent<HTMLDivElement>) => void;
  onPaste?: (event: React.ClipboardEvent<HTMLDivElement>) => void;
  onEnter?: (event: KeyboardEvent | null) => boolean;
  enterKeyHint?: React.HTMLAttributes<HTMLElement>["enterKeyHint"];
  onCompositionStart?: () => void;
  onCompositionEnd?: () => void;
};

type SerializedTokenNode = Spread<
  {
    type: "token";
    tokenType: TokenType;
    tokenValue: string;
    label: string;
    version: 1;
  },
  SerializedTextNode
>;

class TokenNode extends TextNode {
  __tokenType: TokenType;
  __tokenValue: string;
  __label: string;

  static getType(): string {
    return "token";
  }

  static clone(node: TokenNode): TokenNode {
    return new TokenNode(node.__tokenType, node.__tokenValue, node.__label, node.__key);
  }

  static importJSON(serializedNode: SerializedTokenNode): TokenNode {
    return $createTokenNode(
      serializedNode.tokenType,
      serializedNode.tokenValue,
      serializedNode.label
    );
  }

  constructor(tokenType: TokenType, tokenValue: string, label: string, key?: NodeKey) {
    super(label, key);
    this.__tokenType = tokenType;
    this.__tokenValue = tokenValue;
    this.__label = label;
  }

  createDOM(config: EditorConfig): HTMLElement {
    const dom = super.createDOM(config);
    dom.dataset.mindfsTokenNode = "true";
    dom.contentEditable = "false";
    dom.style.display = "inline-flex";
    dom.style.alignItems = "center";
    dom.style.padding = "1px 6px";
    dom.style.margin = "0 1px";
    dom.style.borderRadius = "8px";
    dom.style.whiteSpace = "pre";
    if (this.__tokenType === "file") {
      dom.style.background = "var(--token-file-bg)";
      dom.style.color = "var(--token-file-text)";
    } else {
      dom.style.background = "var(--token-skill-bg)";
      dom.style.color = "var(--token-skill-text)";
    }
    return dom;
  }

  updateDOM(prevNode: TokenNode, dom: HTMLElement, config: EditorConfig): boolean {
    const updated = super.updateDOM(prevNode as unknown as this, dom, config);
    if (prevNode.__tokenType !== this.__tokenType) {
      if (this.__tokenType === "file") {
        dom.style.background = "var(--token-file-bg)";
        dom.style.color = "var(--token-file-text)";
      } else {
        dom.style.background = "var(--token-skill-bg)";
        dom.style.color = "var(--token-skill-text)";
      }
    }
    return updated;
  }

  exportJSON(): SerializedTokenNode {
    return {
      ...super.exportJSON(),
      type: "token",
      tokenType: this.__tokenType,
      tokenValue: this.__tokenValue,
      label: this.__label,
      version: 1,
    };
  }

  getTokenType(): TokenType {
    return this.__tokenType;
  }

  getTokenValue(): string {
    return this.__tokenValue;
  }

  getLabel(): string {
    return this.__label;
  }

  isTextEntity(): true {
    return true;
  }

  canInsertTextBefore(): boolean {
    return false;
  }

  canInsertTextAfter(): boolean {
    return false;
  }
}

function $createTokenNode(type: TokenType, value: string, label: string): TokenNode {
  return new TokenNode(type, value, label);
}

function $isTokenNode(node: unknown): node is TokenNode {
  return node instanceof TokenNode;
}

function createLabel(type: TokenType, value: string): string {
  if (type === "file") {
    const parts = value.replace(/\\/g, "/").split("/");
    return parts[parts.length - 1] || value;
  }
  return value;
}

function serializeEditor(): string {
  const parts: string[] = [];
  const visit = (node: any) => {
    if ($isTokenNode(node)) {
      parts.push(
        node.getTokenType() === "file"
          ? `[file: ${node.getTokenValue()}]`
          : `[use skill: ${node.getTokenValue()}]`
      );
      return;
    }
    if ($isLineBreakNode(node)) {
      parts.push("\n");
      return;
    }
    if ($isTextNode(node)) {
      parts.push(node.getTextContent());
      return;
    }
    if (typeof node.getChildren === "function") {
      for (const child of node.getChildren()) {
        visit(child);
      }
    }
  };
  visit($getRoot());
  return parts.join("");
}

function $insertSerializedTextAtSelection(text: string): boolean {
  if (text === "") {
    return false;
  }
  const pattern = /\[(read file|file|use skill):\s*([^\]]+)\]/g;
  let lastIndex = 0;
  let inserted = false;
  let match: RegExpExecArray | null;

  const insertToken = (type: TokenType, value: string): boolean => {
    let selection = $getSelection();
    if (!$isRangeSelection(selection)) {
      $getRoot().selectEnd();
      selection = $getSelection();
    }
    if (!$isRangeSelection(selection)) {
      return false;
    }
    selection.insertNodes([$createTokenNode(type, value, createLabel(type, value))]);
    return true;
  };

  while ((match = pattern.exec(text)) !== null) {
    const prefix = text.slice(lastIndex, match.index);
    if (prefix) {
      inserted = $insertPlainTextAtSelection(prefix) || inserted;
    }
    const tokenType: TokenType = match[1] === "use skill" ? "skill" : "file";
    const tokenValue = match[2].trim();
    if (tokenValue) {
      inserted = insertToken(tokenType, tokenValue) || inserted;
    }
    lastIndex = pattern.lastIndex;
  }

  const suffix = text.slice(lastIndex);
  if (suffix) {
    inserted = $insertPlainTextAtSelection(suffix) || inserted;
  }
  return inserted;
}

function serializedTextEndsWithToken(text: string): boolean {
  return /\[(?:read file|file|use skill):\s*[^\]]+\]\s*$/.test(text);
}

function $selectAfterTokenNode(node: TokenNode): void {
  const next = node.getNextSibling();
  if ($isTextNode(next) && !$isTokenNode(next)) {
    next.select(next.getTextContentSize(), next.getTextContentSize());
    return;
  }
  const anchor = $createTextNode(" ");
  node.insertAfter(anchor);
  anchor.select(1, 1);
}

function $selectEditorEndWithTokenAnchor(): void {
  const root = $getRoot();
  const lastChild = root.getLastChild();
  if ($isTokenNode(lastChild)) {
    $selectAfterTokenNode(lastChild);
    return;
  }
  root.selectEnd();
}

function getDisplayText(): string {
  return $getRoot().getTextContent();
}

function getActiveTokenFromSelection(): ActiveToken | null {
  const selection = $getSelection();
  if (!$isRangeSelection(selection) || !selection.isCollapsed()) {
    return null;
  }
  const anchorNode = selection.anchor.getNode();
  if (!$isTextNode(anchorNode) || $isTokenNode(anchorNode)) {
    return null;
  }
  const text = anchorNode.getTextContent();
  const offset = selection.anchor.offset;
  return parseActiveToken(text, offset);
}

function parseActiveToken(displayText: string, cursorPos: number): ActiveToken | null {
  const cursor = Math.max(0, Math.min(cursorPos, displayText.length));
  let start = cursor - 1;
  while (start >= 0) {
    const ch = displayText[start];
    if (ch === "@" || ch === "/" || ch === "#") {
      const prev = start > 0 ? displayText[start - 1] : "";
      const isBoundary =
        prev === "" ||
        /\s/.test(prev) ||
        prev === "(" ||
        prev === "[" ||
        prev === "{" ||
        prev === '"' ||
        prev === "'";
      if (!isBoundary) {
        return null;
      }
      let end = cursor;
      for (; end < displayText.length; end++) {
        const next = displayText[end];
        if (/\s/.test(next) || next === "[" || next === "]" || next === "\n") {
          break;
        }
      }
      return {
        type: ch === "@" ? "file" : ch === "/" ? "slash" : "prompt",
        query: displayText.slice(start + 1, end),
      };
    }
    if (/\s/.test(ch) || ch === "[" || ch === "]") {
      return null;
    }
    start--;
  }
  return null;
}

function expectedActiveTokenType(candidateType: CandidateType): ActiveTokenType {
  if (candidateType === "command") {
    return "command";
  }
  if (candidateType === "file") {
    return "file";
  }
  if (candidateType === "prompt") {
    return "prompt";
  }
  return "slash";
}

function triggerChar(tokenType: ActiveTokenType): "@" | "/" | "#" {
  if (tokenType === "file") {
    return "@";
  }
  if (tokenType === "prompt") {
    return "#";
  }
  return "/";
}

function getPasteDataTransfer(event: ClipboardEvent | InputEvent | KeyboardEvent): DataTransfer | null {
  if (typeof ClipboardEvent !== "undefined" && event instanceof ClipboardEvent) {
    return event.clipboardData;
  }
  if (typeof InputEvent !== "undefined" && event instanceof InputEvent) {
    return event.dataTransfer;
  }
  return null;
}

function getPlainTextFromPasteEvent(event: ClipboardEvent | InputEvent | KeyboardEvent): string {
  const dataTransfer = getPasteDataTransfer(event);
  return dataTransfer?.getData("text/plain") || dataTransfer?.getData("text/uri-list") || "";
}

function pasteEventHasFiles(event: ClipboardEvent | InputEvent | KeyboardEvent): boolean {
  const dataTransfer = getPasteDataTransfer(event);
  return Array.from(dataTransfer?.items || []).some((item) => item.kind === "file");
}

function isKeyboardPasteInput(event: InputEvent): boolean {
  // IME 组合态（含语音输入法 AI 整理的刷新式提交）不是粘贴：
  // 只凭 data 里的换行无法区分「真·多行粘贴」与「语音/IME 的分片替换」，
  // 一旦误判成粘贴接管并 preventDefault，输入法的替换/整理动作被砍掉、
  // 只剩最后一个分片 —— 这是语音输入「丢段」的根源。
  // 因此只认硬信号（inputType/dataTransfer），组合类输入一律放行给 Lexical/浏览器原生。
  if (event.isComposing) {
    return false;
  }
  // 组合类 inputType（语音/IME 专属）同样不是粘贴，放行给原生。
  if (isAndroidImeInput(event)) {
    return false;
  }
  return event.inputType === "insertFromPaste"
    || event.inputType === "insertFromPasteAsQuotation"
    || !!event.dataTransfer;
}

function isAndroidWebViewLikeRuntime(): boolean {
  if (typeof window === "undefined" || typeof navigator === "undefined") {
    return false;
  }
  const userAgent = navigator.userAgent || "";
  if (!/Android/i.test(userAgent)) {
    return false;
  }
  const win = window as Window & {
    Capacitor?: unknown;
    __MIND_FS_NATIVE_PLATFORM__?: string;
    MindFSNative?: unknown;
  };
  return !!win.Capacitor
    || String(win.__MIND_FS_NATIVE_PLATFORM__ || "").toLowerCase() === "android"
    || !!win.MindFSNative
    || /\bwv\b/i.test(userAgent);
}

function isAndroidImeInput(event: InputEvent): boolean {
  return event.inputType === "insertCompositionText"
    || event.inputType === "insertFromComposition"
    || event.inputType === "insertReplacementText"
    || event.inputType === "deleteCompositionText"
    || event.inputType === "deleteByComposition";
}

function nodeIsInside(root: Node, node: Node | null): boolean {
  return !!node && (node === root || root.contains(node));
}

function collapseDOMSelectionInside(rootElement: HTMLElement): void {
  const selection = window.getSelection?.();
  if (
    !selection ||
    selection.isCollapsed ||
    !nodeIsInside(rootElement, selection.anchorNode) ||
    !nodeIsInside(rootElement, selection.focusNode)
  ) {
    return;
  }
  selection.collapseToEnd();
}

async function readClipboardTextFallback(): Promise<string> {
  try {
    const mod = await import("@capacitor/clipboard");
    const result = await mod.Clipboard.read();
    if (result.value) {
      return result.value;
    }
  } catch {
    // Fall through to the browser clipboard API.
  }
  try {
    return await navigator.clipboard?.readText?.() || "";
  } catch {
    return "";
  }
}

function $insertPlainTextAtSelection(text: string): boolean {
  if (text === "") {
    return false;
  }
  let selection = $getSelection();
  if (!$isRangeSelection(selection)) {
    $getRoot().selectEnd();
    selection = $getSelection();
  }
  if (!$isRangeSelection(selection)) {
    return false;
  }
  const parts = text.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
  for (let index = 0; index < parts.length; index += 1) {
    const part = parts[index];
    if (part) {
      selection.insertText(part);
    }
    if (index < parts.length - 1) {
      selection.insertLineBreak();
    }
    selection = $getSelection();
    if (!$isRangeSelection(selection)) {
      return false;
    }
  }
  return true;
}

function $replaceWithPlainText(text: string): void {
  const root = $getRoot();
  root.clear();
  root.selectEnd();
  if (text !== "") {
    $insertPlainTextAtSelection(text);
  }
  $getRoot().selectEnd();
}

function $replaceWithSerializedText(text: string): void {
  const root = $getRoot();
  root.clear();
  root.selectEnd();
  if (text !== "") {
    $insertSerializedTextAtSelection(text);
    if (serializedTextEndsWithToken(text)) {
      $insertPlainTextAtSelection(" ");
    }
  }
  $getRoot().selectEnd();
}

function EditorBridge({
  onChange,
  onReady,
  onEnter,
  onDeleteToken,
}: {
  onChange: TokenEditorProps["onChange"];
  onReady: (api: { editor: LexicalEditor; root: HTMLDivElement | null }) => void;
  onEnter?: (event: KeyboardEvent | null) => boolean;
  onDeleteToken: (forward: boolean) => boolean;
}) {
  const [editor] = useLexicalComposerContext();
  const rootRef = useRef<HTMLDivElement | null>(null);
  const [rootElement, setRootElement] = useState<HTMLDivElement | null>(null);
  const androidImeRepairTimerRef = useRef<number | null>(null);
  // [IME-FIX] 见下方 useEffect 里 scheduleMultilineCompositionFix 的注释。
  // 组合开始前的完整 editor state 快照（含 token 节点与选区），用于事后重建。
  const compositionSnapshotRef = useRef<EditorState | null>(null);
  // 组合过程中最后一次 beforeinput 给出的 data —— compositionend 自带的 data 已被证实残缺，
  // 只有这个才是输入法的完整意图。
  const lastCompositionDataRef = useRef("");
  // Lexical 在「组合文本以换行结尾」时会合成一个 event === null 的 KEY_ENTER_COMMAND，
  // 它不是用户按键，不该触发发送。这里记一个短时间窗把它吞掉。
  const suppressSyntheticEnterUntilRef = useRef(0);
  // [IME-DIAG] 页面内诊断浮窗（?imediag=1 开启）：记录最近输入事件与编辑器文本量，用于定位语音输入丢段
  const [diagLines, setDiagLines] = useState<string[]>([]);
  const diagOn = useMemo(() => typeof window !== "undefined" && new URLSearchParams(window.location.search).has("imediag"), []);
  const pushDiag = useCallback((line: string) => {
    if (!diagOn) return;
    setDiagLines((prev) => [...prev.slice(-19), line]);
  }, [diagOn]);
  // [IME-DIAG] 真机上没法看控制台，日志靠这个按钮导出。
  // 注意：dev server 走 http://<局域网IP>:5173，是**非安全上下文**，`navigator.clipboard`
  // 根本不存在；而 `await import(...)` 会先丢掉用户手势（transient activation），
  // 之后再调剪贴板 API 也会被拒。所以同步的 execCommand 路径必须排在最前面。
  const [copyState, setCopyState] = useState<"" | "ok" | "fail">("");
  const [rawOpen, setRawOpen] = useState(false);
  const copyDiag = useCallback(() => {
    const text = diagLines.join("\n");
    if (text === "") return;
    let ok = false;
    try {
      const holder = document.createElement("textarea");
      holder.value = text;
      holder.setAttribute("readonly", "");
      holder.style.position = "fixed";
      holder.style.top = "0";
      holder.style.left = "0";
      holder.style.opacity = "0";
      document.body.appendChild(holder);
      holder.focus({ preventScroll: true });
      holder.setSelectionRange(0, text.length);
      ok = document.execCommand("copy");
      document.body.removeChild(holder);
    } catch {
      ok = false;
    }
    if (ok) {
      setCopyState("ok");
      return;
    }
    // 原生壳内 Capacitor 插件可用；浏览器安全上下文下退回 navigator.clipboard。
    void (async () => {
      try {
        const mod = await import("@capacitor/clipboard");
        await mod.Clipboard.write({ string: text });
        setCopyState("ok");
        return;
      } catch { /* Fall through to the browser clipboard API. */ }
      try {
        await navigator.clipboard.writeText(text);
        setCopyState("ok");
      } catch {
        // 都不行就展开原文，让用户长按全选。
        setCopyState("fail");
        setRawOpen(true);
      }
    })();
  }, [diagLines]);
  useEffect(() => {
    if (copyState === "") return;
    const timer = window.setTimeout(() => setCopyState(""), 2000);
    return () => window.clearTimeout(timer);
  }, [copyState]);

  useEffect(() => {
    return editor.registerRootListener((rootElement) => {
      rootRef.current = rootElement as HTMLDivElement | null;
      setRootElement(rootRef.current);
      onReady({ editor, root: rootRef.current });
    });
  }, [editor, onReady]);

  useEffect(() => {
    if (!rootElement) {
      return;
    }
    const handleTokenPointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof HTMLElement)) {
        return;
      }
      const tokenElement = target.closest<HTMLElement>("[data-mindfs-token-node='true']");
      if (tokenElement && rootElement.contains(tokenElement)) {
        event.preventDefault();
        event.stopPropagation();
        rootElement.focus({ preventScroll: true });
        editor.update(() => {
          const node = $getNearestNodeFromDOMNode(tokenElement);
          if ($isTokenNode(node)) {
            $selectAfterTokenNode(node);
          }
        });
        return;
      }
      if (target !== rootElement) {
        return;
      }
      event.preventDefault();
      rootElement.focus({ preventScroll: true });
      editor.update(() => {
        $selectEditorEndWithTokenAnchor();
      });
    };
    rootElement.addEventListener("pointerdown", handleTokenPointerDown, { capture: true });
    return () => {
      rootElement.removeEventListener("pointerdown", handleTokenPointerDown, { capture: true });
    };
  }, [editor, rootElement]);

  useEffect(() => {
    if (!rootElement) {
      return;
    }
    const scheduleAndroidImeRepair = () => {
      if (!isAndroidWebViewLikeRuntime()) {
        return;
      }
      if (androidImeRepairTimerRef.current !== null) {
        window.clearTimeout(androidImeRepairTimerRef.current);
      }
      const repairSelection = () => {
        editor.update(() => {
          const mutableEditor = editor as LexicalEditor & { _compositionKey?: string | null };
          mutableEditor._compositionKey = null;
          const selection = $getSelection();
          if (!$isRangeSelection(selection) || selection.isCollapsed()) {
            return;
          }
          const focus = selection.focus;
          selection.anchor.set(focus.key, focus.offset, focus.type);
          selection.dirty = true;
        });
        collapseDOMSelectionInside(rootElement);
        rootElement.focus({ preventScroll: true });
      };
      window.requestAnimationFrame(repairSelection);
      androidImeRepairTimerRef.current = window.setTimeout(() => {
        androidImeRepairTimerRef.current = null;
        repairSelection();
      }, 80);
    };
    const insertFromNativePaste = (event: ClipboardEvent | InputEvent) => {
      if (pasteEventHasFiles(event)) {
        return;
      }
      const text = getPlainTextFromPasteEvent(event);
      const inputText = typeof InputEvent !== "undefined" && event instanceof InputEvent ? event.data || "" : "";
      event.preventDefault();
      event.stopPropagation();
      event.stopImmediatePropagation();
      const insert = (nextText: string) => {
        if (nextText === "") {
          return;
        }
        editor.update(() => {
          $insertPlainTextAtSelection(nextText);
        });
        rootElement.focus({ preventScroll: true });
      };
      if (inputText !== "") {
        // InputEvent 的 data 就是这次真正插入的文本（如语音输入法 AI 整理后的
        // 结构化内容），优先用它，避免 dataTransfer/异步读剪贴板把用户之前
        // 复制的旧内容插进来。
        insert(inputText);
        return;
      }
      if (text !== "") {
        insert(text);
        return;
      }
      void readClipboardTextFallback().then((clipboardText) => {
        insert(clipboardText || inputText);
      });
    };
    const handlePaste = (event: ClipboardEvent) => insertFromNativePaste(event);
    // [IME-DIAG] 不可见字符可视化：⟨Z⟩=U+200B(Lexical 组合占位) ⟨N⟩=NBSP ⏎=换行
    const vis = (value: string, max: number) =>
      value
        .replace(/​/g, "⟨Z⟩")
        .replace(/ /g, "⟨N⟩")
        .replace(/\n/g, "⏎")
        .slice(0, max);
    // [IME-DIAG] 抓浏览器插入组合文本时的**原始** DOM 变更。
    // `shape` 是在 input 的捕获阶段读的，但那时 Lexical 的 MutationObserver（microtask）
    // 往往已跑完并按 editor state 重排过 DOM，看不到中间态。MutationRecord 是快照、
    // 不受后续修改影响，所以改用「beforeinput 清队列 → input 里取出」的方式。
    // 注意：回调被触发时队列会被交给回调并清空，所以必须在回调里累积，
    // 只靠 takeRecords() 会漏掉 microtask checkpoint 之前的全部记录。
    let pendingMutations: MutationRecord[] = [];
    const mutationLog = new MutationObserver((records) => {
      pendingMutations = pendingMutations.concat(records);
    });
    if (diagOn) {
      mutationLog.observe(rootElement, {
        characterData: true,
        characterDataOldValue: true,
        childList: true,
        subtree: true,
      });
    }
    const resetMutations = () => {
      mutationLog.takeRecords();
      pendingMutations = [];
    };
    const describeMutations = () => {
      const records = pendingMutations.concat(mutationLog.takeRecords());
      pendingMutations = [];
      if (records.length === 0) return "(none)";
      const shown = records.slice(0, 6).map((record) => {
        if (record.type === "characterData") {
          const target = record.target as CharacterData;
          return `~"${vis(record.oldValue || "", 18)}"→"${vis(target.nodeValue || "", 18)}"`;
        }
        const added = Array.from(record.addedNodes).map((node) => `+${node.nodeName}"${vis(node.textContent || "", 14)}"`);
        const removed = Array.from(record.removedNodes).map((node) => `-${node.nodeName}"${vis(node.textContent || "", 14)}"`);
        return [...added, ...removed].join(" ") || "(childList)";
      });
      return shown.join(" ") + (records.length > 6 ? ` …+${records.length - 6}` : "");
    };
    // [IME-DIAG] beforeinput 的 targetRanges = 浏览器打算替换掉的区间。
    // 丢段时它是否只覆盖了其中一段，直接说明 WebView 的意图。
    const targetRangeInfo = (event: InputEvent) => {
      const ranges = typeof event.getTargetRanges === "function" ? event.getTargetRanges() : [];
      if (ranges.length === 0) return "none";
      const range = ranges[0];
      const start = range.startContainer;
      const startText = start.nodeType === Node.TEXT_NODE ? start.nodeValue || "" : `<${start.nodeName}>`;
      const sameContainer = range.startContainer === range.endContainer;
      return `${vis(startText, 20)}[${range.startOffset},${range.endOffset}]${sameContainer ? "" : "*跨节点*"}`;
    };
    const currentText = () => {
      let t = "";
      try { editor.getEditorState().read(() => { t = $getRoot().getTextContent(); }); } catch { /* ignore */ }
      return `${t.length}:${vis(t, 60)}`;
    };
    const currentDom = () => (rootElement ? vis(rootElement.innerText, 60) : "");
    // [IME-DIAG] DOM 结构摘要：验证浏览器是否把含换行的组合文本拆成了多个块/文本节点。
    // 形如 `P[SPAN12,BR0,SPAN8]` 表示段落里被插了 <br> 且文本分成两个节点。
    const domShape = () => {
      if (!rootElement) return "";
      const describe = (node: ChildNode): string => {
        if (node.nodeType === Node.TEXT_NODE) return `#t${(node.nodeValue || "").length}`;
        return `${node.nodeName}${(node.textContent || "").length}`;
      };
      return Array.from(rootElement.childNodes)
        .map((block) => {
          if (block.nodeType === Node.TEXT_NODE) return describe(block);
          const kids = Array.from(block.childNodes).map(describe).join(",");
          return `${block.nodeName}[${kids}]`;
        })
        .join("|")
        .slice(0, 90);
    };
    // [IME-DIAG] Lexical 的 $updateSelectedTextFromDOM 只读 domSelection.anchorNode 这一个
    // 文本节点，并用它整体覆盖组合 TextNode。这里打印它，确认丢段时它是否只剩一段。
    const anchorInfo = () => {
      const selection = window.getSelection();
      const node = selection?.anchorNode;
      if (!selection || !node) return "none";
      const value = node.nodeType === Node.TEXT_NODE ? node.nodeValue || "" : `<${node.nodeName}>`;
      return `${vis(value, 40)}@${selection.anchorOffset}`;
    };
    // [IME-DIAG] 祖先捕获阶段先于 rootElement 上的任何监听器（含 Lexical 自身），
    // 用于验证「组合开始前的干净快照」是否拿得到——后续修复方案依赖它。
    // [IME-FIX] 语音输入「说一段话、输入法 AI 整理成多段」时丢段的修复。
    //
    // 根因在 Lexical 侧，且拦不住：
    //   1. `insertCompositionText` 的 beforeinput 被 Lexical 直接放行（Lexical.dev.mjs
    //      的 onBeforeInput），且按 Input Events 规范该事件不可取消，preventDefault 无效。
    //   2. 浏览器把 data 里的 `\n` 落成 DOM 时会新建节点（<br> / 第二个文本节点）。
    //   3. Lexical 的 MutationObserver → flushMutations 把「editor state 里没有的 DOM」
    //      一律 removeChild 掉（源码注释：Lexical 的 editor state 是 source of truth），
    //      同时用残留的那一个文本节点覆盖 TextNode —— 换行后的那一段就此消失。
    //      这一步在 microtask 里跑，早于 input 事件派发，事件层面无从拦截。
    //   4. compositionend 时输入法自己给的 data 也已残缺，且以 `\n` 结尾会命中
    //      $onCompositionEndImpl 的分支：合成一个 KEY_ENTER_COMMAND 后直接 return，
    //      跳过最后的文本落库。
    //
    // 所以只能事后重建：组合开始前存一份 editor state 快照，组合结束后若发现内容被吞，
    // 就恢复快照并用「最后一次 beforeinput 的完整 data」重新插入。用 setEditorState 而不是
    // 拼字符串，是为了原样保住 token 节点与选区。只在 data 含 `\n` 时激活，普通输入不走这条路。
    const scheduleMultilineCompositionFix = (data: string) => {
      const snapshot = compositionSnapshotRef.current;
      compositionSnapshotRef.current = null;
      // setEditorState 不接受空状态（会 invariant 抛错）。
      if (!snapshot || snapshot.isEmpty() || data === "") {
        return;
      }
      // 等到 Lexical 的 compositionend 处理与 React 的 onCompositionEnd 都跑完再动手：
      // 我们挂在 capture 阶段，比它们都早，同步改会被随后覆盖。
      window.setTimeout(() => {
        let current = "";
        editor.getEditorState().read(() => { current = $getRoot().getTextContent(); });
        // 内容已完整就不动——避免在没出问题的机型上白白重建一次状态。
        if (current.includes(data)) {
          if (diagOn) pushDiag(`FIX skipped (intact) txt="${currentText()}"`);
          return;
        }
        editor.setEditorState(snapshot);
        editor.update(() => {
          // 组合已经结束，但 Lexical 可能还留着 compositionKey（Android 分支会提前清、
          // 其它路径不一定），带着它插入会让 reconciler 再加一次组合占位符。
          $setCompositionKey(null);
          $insertPlainTextAtSelection(data);
        });
        rootElement.focus({ preventScroll: true });
        if (diagOn) pushDiag(`FIX applied data="${vis(data, 40)}" txt="${currentText()}"`);
      }, 0);
    };
    const handleCompositionStartAhead = () => {
      lastCompositionDataRef.current = "";
      // 在祖先的捕获阶段取快照，早于 Lexical 自己的 compositionstart（它会插入
      // 组合占位符并设 compositionKey），拿到的才是干净的组合前状态。
      compositionSnapshotRef.current = editor.getEditorState();
      if (!diagOn) return;
      resetMutations();
      pushDiag(`CS(ahead) txt="${currentText()}" shape=${domShape()}`);
    };
    const handleCompositionStart = () => {
      if (!diagOn) return;
      pushDiag(`CS(after) txt="${currentText()}" shape=${domShape()}`);
    };
    // [IME-DIAG] 记录最后一次 beforeinput 的 data：compositionend 自带的 data 已被证实
    // 不可靠（只剩第一段），真要补全内容只能靠这个。
    const handleBeforeInput = (event: InputEvent) => {
      if (event.isComposing && event.data) {
        lastCompositionDataRef.current = event.data;
      }
      if (diagOn) {
        // 清空队列，让随后 input 里取到的只是这次插入产生的变更
        resetMutations();
        pushDiag(
          `BI:${event.inputType} comp=${event.isComposing} cancelable=${event.cancelable}` +
          ` data="${vis(event.data || "", 60)}" txt="${currentText()}" tr=${targetRangeInfo(event)}`
        );
      }
      if (isKeyboardPasteInput(event)) {
        insertFromNativePaste(event);
        return;
      }
      // [IME-FIX-TEST] 组合过程中不再跑 scheduleAndroidImeRepair（它清 _compositionKey + 移 selection，
      // 会破坏 Lexical 组合文本跟踪、导致多段语音输入丢段）。验证是否解决丢段。
    };
    const handleInput = (event: Event) => {
      if (!diagOn) return;
      if (typeof InputEvent !== "undefined" && event instanceof InputEvent && isAndroidImeInput(event)) {
        pushDiag(
          `IN:${event.inputType} data="${vis(event.data || "", 60)}" txt="${currentText()}"` +
          ` anchor="${anchorInfo()}" shape=${domShape()} mut=${describeMutations()}`
        );
      }
    };
    const handleCompositionEnd = (event: Event) => {
      const endData = typeof CompositionEvent !== "undefined" && event instanceof CompositionEvent
        ? event.data || ""
        : "";
      // compositionend 自带的 data 会残缺（实测只剩第一段），优先用组合过程中
      // 最后一次 beforeinput 的 data —— 那才是输入法给出的完整文本。
      const fullData = lastCompositionDataRef.current || endData;
      lastCompositionDataRef.current = "";
      const needsMultilineFix = fullData.includes("\n");
      if (diagOn) {
        pushDiag(
          `CE data="${vis(endData, 60)}" full="${vis(fullData, 60)}" fix=${needsMultilineFix}` +
          ` txt="${currentText()}" shape=${domShape()}`
        );
      }
      if (needsMultilineFix) {
        // Lexical 随后会因 data 以 `\n` 结尾合成一个 KEY_ENTER_COMMAND，吞掉它。
        suppressSyntheticEnterUntilRef.current = performance.now() + 600;
        // 重建会自己把选区放到正确位置，不需要（也不能让）selection 修补插一脚。
        if (androidImeRepairTimerRef.current !== null) {
          window.clearTimeout(androidImeRepairTimerRef.current);
          androidImeRepairTimerRef.current = null;
        }
        scheduleMultilineCompositionFix(fullData);
      } else {
        compositionSnapshotRef.current = null;
        scheduleAndroidImeRepair();
      }
      if (diagOn) {
        // Lexical 的 $onCompositionEndImpl 与后续修复都会再改一次，这里复查最终落定的内容。
        window.requestAnimationFrame(() => {
          window.setTimeout(() => {
            pushDiag(`CE+1 txt="${currentText()}" dom="${currentDom()}" shape=${domShape()}`);
          }, 0);
        });
      }
    };
    const compositionStartHost: EventTarget = rootElement.parentElement ?? document;
    compositionStartHost.addEventListener("compositionstart", handleCompositionStartAhead, { capture: true });
    rootElement.addEventListener("compositionstart", handleCompositionStart, { capture: true });
    rootElement.addEventListener("paste", handlePaste, { capture: true });
    rootElement.addEventListener("beforeinput", handleBeforeInput, { capture: true });
    rootElement.addEventListener("input", handleInput, { capture: true });
    rootElement.addEventListener("compositionend", handleCompositionEnd, { capture: true });
    return () => {
      if (androidImeRepairTimerRef.current !== null) {
        window.clearTimeout(androidImeRepairTimerRef.current);
        androidImeRepairTimerRef.current = null;
      }
      mutationLog.disconnect();
      compositionStartHost.removeEventListener("compositionstart", handleCompositionStartAhead, { capture: true });
      rootElement.removeEventListener("compositionstart", handleCompositionStart, { capture: true });
      rootElement.removeEventListener("paste", handlePaste, { capture: true });
      rootElement.removeEventListener("beforeinput", handleBeforeInput, { capture: true });
      rootElement.removeEventListener("input", handleInput, { capture: true });
      rootElement.removeEventListener("compositionend", handleCompositionEnd, { capture: true });
    };
  }, [editor, rootElement, pushDiag, diagOn]);

  useEffect(() => {
    let lastLen = -1;
    return editor.registerUpdateListener(({ editorState }) => {
      editorState.read(() => {
        const displayText = getDisplayText();
        if (diagOn && displayText.length !== lastLen && lastLen !== -1) {
          pushDiag(`TXT ${lastLen}->${displayText.length} "${displayText.slice(0, 30)}"`);
        }
        lastLen = displayText.length;
        onChange({
          serializedText: serializeEditor(),
          displayText,
          activeToken: getActiveTokenFromSelection(),
        });
      });
    });
  }, [editor, onChange, diagOn, pushDiag]);

  useEffect(() => {
    return editor.registerCommand(
      PASTE_COMMAND,
      (event) => {
        if (pasteEventHasFiles(event)) {
          return false;
        }
        const text = getPlainTextFromPasteEvent(event);
        if (text === "") {
          return false;
        }
        event.preventDefault();
        if ($insertPlainTextAtSelection(text)) {
          return true;
        }
        return false;
      },
      COMMAND_PRIORITY_HIGH
    );
  }, [editor]);

  useEffect(() => {
    return editor.registerCommand(
      KEY_ENTER_COMMAND,
      (event) => {
        // [IME-FIX] event === null 不是用户按键，而是 Lexical 的 $onCompositionEndImpl
        // 在「组合文本以换行结尾」时合成 dispatch 的。它会打到发送逻辑上（目前只是靠
        // ActionBar 里 isComposingRef 的事件顺序侥幸挡住），而换行已由多段修复自己插入，
        // 所以这里直接吞掉。
        if (event === null && performance.now() < suppressSyntheticEnterUntilRef.current) {
          if (diagOn) {
            pushDiag("ENTER(synthetic) suppressed");
          }
          return true;
        }
        return onEnter?.(event) ?? false;
      },
      COMMAND_PRIORITY_HIGH
    );
  }, [editor, onEnter, diagOn, pushDiag]);

  useEffect(() => {
    return editor.registerCommand(
      KEY_BACKSPACE_COMMAND,
      () => onDeleteToken(false),
      COMMAND_PRIORITY_HIGH
    );
  }, [editor, onDeleteToken]);

  useEffect(() => {
    return editor.registerCommand(
      KEY_DELETE_COMMAND,
      () => onDeleteToken(true),
      COMMAND_PRIORITY_HIGH
    );
  }, [editor, onDeleteToken]);

  if (!diagOn) {
    return null;
  }
  // [IME-DIAG] 定位语音输入丢段的可视诊断浮窗（URL ?imediag=1 开启）
  return createPortal(
    <div style={{
      position: "fixed",
      right: 8,
      bottom: 8,
      zIndex: 2147483647,
      maxWidth: 360,
      maxHeight: "56vh",
      overflowY: "auto",
      background: "rgba(0,0,0,0.85)",
      color: "#7cfc00",
      fontFamily: "monospace",
      fontSize: 11,
      lineHeight: 1.4,
      padding: 6,
      borderRadius: 4,
      whiteSpace: "pre-wrap",
      wordBreak: "break-all",
    }}>
      <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 4 }}>
        <span style={{ color: "#fff", fontWeight: 700 }}>[IME-DIAG]</span>
        <button
          type="button"
          onPointerDown={(event) => event.preventDefault()}
          onClick={copyDiag}
          style={{ font: "inherit", color: "#fff", background: "#333", border: "1px solid #666", borderRadius: 3, padding: "0 6px" }}
        >
          {copyState === "ok" ? "已复制" : copyState === "fail" ? "失败" : "复制"}
        </button>
        <button
          type="button"
          onPointerDown={(event) => event.preventDefault()}
          onClick={() => setRawOpen((prev) => !prev)}
          style={{ font: "inherit", color: "#fff", background: "#333", border: "1px solid #666", borderRadius: 3, padding: "0 6px" }}
        >
          {rawOpen ? "收起" : "原文"}
        </button>
        <button
          type="button"
          onPointerDown={(event) => event.preventDefault()}
          onClick={() => { setDiagLines([]); setRawOpen(false); }}
          style={{ font: "inherit", color: "#fff", background: "#333", border: "1px solid #666", borderRadius: 3, padding: "0 6px" }}
        >
          清空
        </button>
      </div>
      {rawOpen ? (
        // 剪贴板全被拒时的兜底：可长按全选的原文框
        <textarea
          readOnly
          value={diagLines.join("\n")}
          onFocus={(event) => event.currentTarget.select()}
          style={{
            width: "100%",
            height: 200,
            font: "inherit",
            color: "#7cfc00",
            background: "#000",
            border: "1px solid #666",
            borderRadius: 3,
          }}
        />
      ) : diagLines.length === 0 ? "(等待输入事件…)" : diagLines.join("\n")}
    </div>,
    document.body
  );
}

const TokenEditor = forwardRef<TokenEditorHandle, TokenEditorProps>(function TokenEditor(
  {
    placeholder,
    disabled = false,
    isDark = false,
    rightInset = 120,
    topInset = 0,
    bottomInset = 12,
    fillHeight = false,
    onChange,
    onFocusChange,
    onPointerDown,
    onKeyDown,
    onPaste,
    onEnter,
    enterKeyHint,
    onCompositionStart,
    onCompositionEnd,
  },
  ref
) {
  const editorRef = useRef<LexicalEditor | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const [isEmpty, setIsEmpty] = useState(true);
  const [isFocused, setIsFocused] = useState(false);
  const isSingleLine = bottomInset <= 12;

  const initialConfig = useMemo(
    () => ({
      namespace: "mindfs-token-editor",
      theme: {},
      onError(error: Error) {
        throw error;
      },
      nodes: [TokenNode],
    }),
    []
  );

  useImperativeHandle(ref, () => ({
    focus() {
      rootRef.current?.focus({ preventScroll: true });
    },
    blur() {
      rootRef.current?.blur();
    },
    getHeight() {
      return rootRef.current?.scrollHeight || 44;
    },
    clear() {
      editorRef.current?.update(() => {
        $setCompositionKey(null);
        $replaceWithPlainText("");
      });
    },
    setText(value: string) {
      editorRef.current?.update(() => {
        $replaceWithSerializedText(value);
      });
      rootRef.current?.focus({ preventScroll: true });
    },
    insertCandidate(type: CandidateType, value: string) {
      const editor = editorRef.current;
      if (!editor) return;
      if (type === "command") {
        editor.update(() => {
          $replaceWithPlainText(value);
        });
        rootRef.current?.focus({ preventScroll: true });
        return;
      }
      editor.update(() => {
        const selection = $getSelection();
        if (!$isRangeSelection(selection) || !selection.isCollapsed()) {
          return;
        }
        const anchorNode = selection.anchor.getNode();
        if (!$isTextNode(anchorNode) || $isTokenNode(anchorNode)) {
          return;
        }
        const text = anchorNode.getTextContent();
        const offset = selection.anchor.offset;
        const token = parseActiveToken(text, offset);
        const expectedType = expectedActiveTokenType(type);
        if (!token || token.type !== expectedType) {
          return;
        }
        let start = offset - 1;
        while (start >= 0 && text[start] !== triggerChar(token.type)) {
          start--;
        }
        if (start < 0) {
          return;
        }
        let end = offset;
        while (end < text.length) {
          const ch = text[end];
          if (/\s/.test(ch) || ch === "[" || ch === "]" || ch === "\n") {
            break;
          }
          end++;
        }
        const prefix = text.slice(0, start);
        const suffix = text.slice(end);
        const replacementNodes = [];
        if (prefix) replacementNodes.push($createTextNode(prefix));
        if (type === "slash_command") {
          replacementNodes.push($createTextNode(`/${value}`));
        } else if (type === "prompt") {
          replacementNodes.push($createTextNode(value));
        } else {
          replacementNodes.push($createTokenNode(type, value, createLabel(type, value)));
        }
        const tailNode = $createTextNode(" ");
        replacementNodes.push(tailNode);
        if (suffix) replacementNodes.push($createTextNode(suffix));
        let current = replacementNodes[0];
        anchorNode.replace(current);
        for (let i = 1; i < replacementNodes.length; i++) {
          current.insertAfter(replacementNodes[i]);
          current = replacementNodes[i];
        }
        tailNode.select(1, 1);
      });
      rootRef.current?.focus({ preventScroll: true });
    },
  }));

  useEffect(() => {
    const root = rootRef.current;
    if (!root) {
      return;
    }
    if (enterKeyHint) {
      root.setAttribute("enterkeyhint", enterKeyHint);
      return;
    }
    root.removeAttribute("enterkeyhint");
  }, [enterKeyHint]);

  const handleChange = (payload: { serializedText: string; displayText: string; activeToken: ActiveToken | null }) => {
    setIsEmpty(payload.displayText.length === 0);
    onChange(payload);
  };

  const handleDeleteToken = (forward: boolean) => {
    const editor = editorRef.current;
    if (!editor) {
      return false;
    }
    let handled = false;
    editor.update(() => {
      const moveSelectionToTextEdge = (node: TextNode | null, atStart: boolean) => {
        if (!node) {
          $getRoot().selectEnd();
          return;
        }
        if (atStart) {
          node.select(0, 0);
          return;
        }
        const size = node.getTextContentSize();
        node.select(size, size);
      };

      const selection = $getSelection();
      if (!$isRangeSelection(selection)) {
        return;
      }
      if (!selection.isCollapsed()) {
        selection.removeText();
        handled = true;
        return;
      }
      const anchorNode = selection.anchor.getNode();
      const anchorOffset = selection.anchor.offset;
      if ($isTokenNode(anchorNode)) {
        const target = forward ? anchorNode.getNextSibling() : anchorNode.getPreviousSibling();
        anchorNode.remove();
        moveSelectionToTextEdge($isTextNode(target) ? target : null, forward);
        handled = true;
        return;
      }
      const textNode = $isTextNode(anchorNode) ? anchorNode : null;
      if (!textNode || $isTokenNode(textNode)) {
        return;
      }
      const sibling = forward
        ? anchorOffset === textNode.getTextContentSize()
          ? textNode.getNextSibling()
          : null
        : anchorOffset === 0
        ? textNode.getPreviousSibling()
        : null;
      if (!$isTokenNode(sibling)) {
        return;
      }
      const target = forward ? sibling.getNextSibling() : sibling.getPreviousSibling();
      sibling.remove();
      if ($isTextNode(target)) {
        moveSelectionToTextEdge(target, forward);
      } else {
        textNode.select(anchorOffset, anchorOffset);
      }
      handled = true;
    });
    return handled;
  };

  return (
    <div
      onMouseDown={onPointerDown}
      onTouchStart={onPointerDown}
      style={{
        position: "relative",
        width: "100%",
        height: fillHeight ? "100%" : undefined,
        minHeight: "44px",
        ["--token-file-bg" as any]: isDark ? "rgba(59,130,246,0.16)" : "rgba(59,130,246,0.10)",
        ["--token-file-text" as any]: isDark ? "#93c5fd" : "#1d4ed8",
        ["--token-skill-bg" as any]: isDark ? "rgba(139,92,246,0.18)" : "rgba(139,92,246,0.10)",
        ["--token-skill-text" as any]: isDark ? "#c4b5fd" : "#7c3aed",
      }}
    >
      <LexicalComposer initialConfig={initialConfig}>
        <PlainTextPlugin
          contentEditable={
            <ContentEditable
              className="token-editor-input"
              aria-placeholder={placeholder}
              placeholder={<span></span>}
              spellCheck={false}
              onFocus={() => {
                setIsFocused(true);
                onFocusChange?.(true);
              }}
              onBlur={() => {
                setIsFocused(false);
                onFocusChange?.(false);
              }}
              onKeyDown={onKeyDown}
              onPaste={onPaste}
              enterKeyHint={enterKeyHint}
              onCompositionStart={onCompositionStart}
              onCompositionEnd={onCompositionEnd}
              style={{
                width: "100%",
                minHeight: fillHeight ? "100%" : isSingleLine ? "44px" : "20px",
                height: fillHeight ? "100%" : isSingleLine ? "44px" : "auto",
                maxHeight: fillHeight ? "none" : "240px",
                overflowY: "auto",
                padding: isSingleLine
                  ? `${12 + topInset}px ${rightInset}px 12px 14px`
                  : `${8 + topInset}px ${rightInset}px ${bottomInset}px 14px`,
                outline: "none",
                fontSize: "16px",
                lineHeight: "20px",
                boxSizing: "border-box",
                whiteSpace: "pre-wrap",
                wordBreak: "break-word",
                color: "var(--text-primary)",
                position: "relative",
                zIndex: 2,
                pointerEvents: disabled ? "none" : "auto",
              }}
            />
          }
          placeholder={
            isEmpty && !isFocused ? (
              <div
                style={{
                  position: "absolute",
                  left: "14px",
                  right: `${rightInset}px`,
                  top: topInset > 0 ? `${topInset + 12}px` : "50%",
                  transform: topInset > 0 ? "none" : "translateY(-50%)",
                  color: "var(--text-secondary)",
                  fontSize: "16px",
                  pointerEvents: "none",
                  zIndex: 1,
                  whiteSpace: "nowrap",
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                }}
              >
                {placeholder}
              </div>
            ) : null
          }
          ErrorBoundary={({ children, onError: _onError }) => children}
        />
        <HistoryPlugin />
        <EditorBridge
          onChange={handleChange}
          onReady={({ editor, root }) => {
            editorRef.current = editor;
            rootRef.current = root;
            if (root && enterKeyHint) {
              root.setAttribute("enterkeyhint", enterKeyHint);
            }
          }}
          onEnter={onEnter}
          onDeleteToken={handleDeleteToken}
        />
      </LexicalComposer>
    </div>
  );
});

export default TokenEditor;
