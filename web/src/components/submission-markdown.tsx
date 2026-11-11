"use client";

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

// A report can contain hostile model output or captured page text. In this
// preview links are text and images/HTML are omitted: opening a draft cannot
// contact an evidence URL or execute captured content.
export function SubmissionMarkdown({ text }: { text: string }) {
  return (
    <div className="min-w-0 space-y-3 break-words text-sm [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_code]:break-all [&_code]:font-mono [&_h1]:font-semibold [&_h1]:text-lg [&_h2]:pt-3 [&_h2]:font-semibold [&_h3]:font-medium [&_li]:my-1 [&_ol]:list-decimal [&_ol]:pl-5 [&_p]:leading-relaxed [&_pre]:overflow-auto [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-3 [&_pre]:text-xs [&_ul]:list-disc [&_ul]:pl-5">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        allowedElements={[
          "p",
          "h1",
          "h2",
          "h3",
          "h4",
          "strong",
          "em",
          "del",
          "ul",
          "ol",
          "li",
          "blockquote",
          "pre",
          "code",
          "br",
          "hr",
        ]}
        unwrapDisallowed
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
