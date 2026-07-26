"use client";

import * as React from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

// Tailwind-styled element overrides (no typography plugin in this project, so we
// style each element). `node` is stripped — it's not a valid DOM attribute.
const components: Components = {
  h1: ({ node, ...p }) => <div className="mt-2.5 text-base font-semibold" {...p} />,
  h2: ({ node, ...p }) => <div className="mt-2 text-sm font-semibold" {...p} />,
  h3: ({ node, ...p }) => <div className="mt-1.5 text-sm font-medium text-foreground/90" {...p} />,
  h4: ({ node, ...p }) => <div className="mt-1.5 text-sm font-medium text-foreground/90" {...p} />,
  p: ({ node, ...p }) => <p className="leading-relaxed" {...p} />,
  ul: ({ node, ...p }) => <ul className="my-1 list-disc space-y-0.5 pl-5" {...p} />,
  ol: ({ node, ...p }) => <ol className="my-1 list-decimal space-y-0.5 pl-5" {...p} />,
  strong: ({ node, ...p }) => <strong className="font-semibold" {...p} />,
  a: ({ node, ...p }) => (
    <a className="text-primary underline underline-offset-2" target="_blank" rel="noreferrer" {...p} />
  ),
  hr: () => <hr className="my-2 border-border" />,
  blockquote: ({ node, ...p }) => (
    <blockquote className="my-1 border-l-2 border-border pl-2.5 text-muted-foreground" {...p} />
  ),
  pre: ({ node, ...p }) => (
    <pre className="my-1.5 overflow-auto rounded-md bg-muted/70 p-2.5 text-xs leading-relaxed" {...p} />
  ),
  code: ({ node, className, children, ...rest }) => {
    const block = /language-/.test(className || "");
    return block ? (
      <code className={"font-mono text-xs " + (className || "")} {...rest}>
        {children}
      </code>
    ) : (
      <code className="break-all rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]" {...rest}>
        {children}
      </code>
    );
  },
  table: ({ node, ...p }) => (
    <div className="my-1.5 overflow-x-auto">
      <table className="w-full border-collapse text-xs" {...p} />
    </div>
  ),
  th: ({ node, ...p }) => (
    <th className="border border-border bg-muted/60 px-2 py-1 text-left font-medium" {...p} />
  ),
  td: ({ node, ...p }) => <td className="border border-border px-2 py-1 align-top" {...p} />,
};

export function Markdown({ text }: { text: string }) {
  return (
    <div className="min-w-0 space-y-1.5 text-sm [&>*:first-child]:mt-0">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
}
