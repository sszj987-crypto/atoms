import { ReactNode, useLayoutEffect, useRef } from "react";

export function RunProgressPanel({ children }: { children: ReactNode }) {
  const panelRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    const panel = panelRef.current;
    const content = contentRef.current;
    if (!panel || !content) return;
    let frame = 0;
    const scrollToBottom = () => { panel.scrollTop = panel.scrollHeight; };
    const follow = () => {
      scrollToBottom();
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(scrollToBottom);
    };
    // Observe the content as well as the capped viewport: content can grow
    // while the viewport stays the same size (details, fonts, or new steps).
    const resize = new ResizeObserver(follow);
    resize.observe(panel);
    resize.observe(content);
    const mutation = new MutationObserver(follow);
    mutation.observe(content, { childList: true, subtree: true, characterData: true, attributes: true });
    follow();
    return () => {
      cancelAnimationFrame(frame);
      resize.disconnect();
      mutation.disconnect();
    };
  }, []);

  return <div ref={panelRef} className="run-progress" aria-live="polite"><div ref={contentRef} className="run-progress-content">{children}</div></div>;
}
