import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

interface HelpProps {
  title: string;
  what: string;
  defaultVal?: string;
  when?: string;
}

type OpenState = "closed" | "hover" | "sticky";

interface Coords { top: number; left: number; flipped: boolean; arrowLeft: number; }

const CLOSE_DELAY_MS = 120;
const OFFSET_PX = 8;
const VIEWPORT_PAD_PX = 8;
const DEFAULT_WIDTH_PX = 280;

export function Help({ title, what, defaultVal, when }: HelpProps) {
  const [state, setState] = useState<OpenState>("closed");
  const [coords, setCoords] = useState<Coords | null>(null);
  const anchorRef = useRef<HTMLSpanElement | null>(null);
  const popoverRef = useRef<HTMLDivElement | null>(null);
  const closeTimerRef = useRef<number | null>(null);

  const cancelClose = useCallback(() => {
    if (closeTimerRef.current !== null) {
      window.clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  }, []);

  const scheduleClose = useCallback(() => {
    cancelClose();
    closeTimerRef.current = window.setTimeout(() => {
      setState(prev => (prev === "sticky" ? prev : "closed"));
      closeTimerRef.current = null;
    }, CLOSE_DELAY_MS);
  }, [cancelClose]);

  const computePosition = useCallback(() => {
    const anchor = anchorRef.current;
    if (!anchor) return;
    const ar = anchor.getBoundingClientRect();
    const pop = popoverRef.current;
    const pw = pop?.offsetWidth ?? DEFAULT_WIDTH_PX;
    const ph = pop?.offsetHeight ?? 100;
    const vw = window.innerWidth;
    const vh = window.innerHeight;

    let left = ar.left - OFFSET_PX;
    let top = ar.bottom + OFFSET_PX;
    let flipped = false;

    if (left + pw > vw - VIEWPORT_PAD_PX) left = vw - VIEWPORT_PAD_PX - pw;
    if (left < VIEWPORT_PAD_PX) left = VIEWPORT_PAD_PX;
    if (top + ph > vh - VIEWPORT_PAD_PX) {
      top = ar.top - ph - OFFSET_PX;
      flipped = true;
    }
    if (top < VIEWPORT_PAD_PX) top = VIEWPORT_PAD_PX;

    const anchorMidX = ar.left + ar.width / 2;
    const arrowLeft = Math.max(8, Math.min(pw - 18, anchorMidX - left - 5));
    setCoords({ top, left, flipped, arrowLeft });
  }, []);

  useLayoutEffect(() => {
    if (state === "closed") { setCoords(null); return; }
    computePosition();
    const onResize = () => computePosition();
    const onScroll = () => computePosition();
    window.addEventListener("resize", onResize);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      window.removeEventListener("resize", onResize);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, [state, computePosition]);

  useLayoutEffect(() => {
    if (state !== "closed" && popoverRef.current) {
      // After first paint we know the real popover dimensions — re-measure once.
      computePosition();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state]);

  useEffect(() => {
    if (state === "closed") return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setState("closed"); };
    const onMouseDown = (e: MouseEvent) => {
      if (state !== "sticky") return;
      const t = e.target as Node;
      if (anchorRef.current?.contains(t)) return;
      if (popoverRef.current?.contains(t)) return;
      setState("closed");
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onMouseDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onMouseDown);
    };
  }, [state]);

  useEffect(() => () => cancelClose(), [cancelClose]);

  const onAnchorEnter = () => { cancelClose(); if (state === "closed") setState("hover"); };
  const onAnchorLeave = () => { if (state === "hover") scheduleClose(); };
  const onPopoverEnter = () => cancelClose();
  const onPopoverLeave = () => { if (state === "hover") scheduleClose(); };
  const onAnchorFocus = () => { cancelClose(); if (state === "closed") setState("hover"); };
  const onAnchorBlur = () => { if (state === "hover") scheduleClose(); };
  const onAnchorClick = () => {
    cancelClose();
    setState(s => (s === "sticky" ? "closed" : "sticky"));
  };
  const onAnchorKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onAnchorClick();
    }
  };

  const portalRoot = typeof document !== "undefined" ? document.getElementById("popover-root") : null;
  const popover = state !== "closed" && coords && portalRoot
    ? createPortal(
        <div
          ref={popoverRef}
          className="shp-popover"
          data-flipped={coords.flipped ? "true" : "false"}
          role="tooltip"
          tabIndex={-1}
          style={{
            position: "fixed",
            top: coords.top,
            left: coords.left,
            width: DEFAULT_WIDTH_PX,
            ["--shp-arrow-left" as never]: `${coords.arrowLeft}px`,
          }}
          onMouseEnter={onPopoverEnter}
          onMouseLeave={onPopoverLeave}
        >
          <h5>{title}</h5>
          <p>{what}</p>
          {defaultVal && (
            <dl>
              <dt>Default</dt>
              <dd>{defaultVal}</dd>
            </dl>
          )}
          {when && <p style={{ marginTop: 6 }}>{when}</p>}
        </div>,
        portalRoot,
      )
    : null;

  return (
    <span style={{ position: "relative", display: "inline-flex" }}>
      <span
        ref={anchorRef}
        className="shp-help"
        data-open={state !== "closed"}
        role="button"
        tabIndex={0}
        onMouseEnter={onAnchorEnter}
        onMouseLeave={onAnchorLeave}
        onFocus={onAnchorFocus}
        onBlur={onAnchorBlur}
        onClick={onAnchorClick}
        onKeyDown={onAnchorKeyDown}
      >
        ?
      </span>
      {popover}
    </span>
  );
}
