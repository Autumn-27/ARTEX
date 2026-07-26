"use client";

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon } from "lucide-react";

import { AgentEditor } from "@/components/agent-editor";
import { Button } from "@/components/ui/button";

// Deep-link editor page for one agent. The primary flow is the drawer on
// /system/agents; this page reuses the same AgentEditor component full-width so a
// direct URL (or an external link) still opens the editor.
function AgentDetailInner() {
  const searchParams = useSearchParams();
  const key = searchParams.get("key") ?? "";

  return (
    <div className="flex flex-1 flex-col gap-4">
      <div className="flex items-center gap-2">
        <Button asChild variant="ghost" size="icon-sm">
          <Link href="/system/agents">
            <ArrowLeftIcon className="size-4" />
          </Link>
        </Button>
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Agent</h1>
          <p className="text-muted-foreground font-mono text-xs">{key}</p>
        </div>
      </div>
      <div className="flex min-h-0 flex-1 flex-col rounded-lg border">
        <AgentEditor agentKey={key} />
      </div>
    </div>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function AgentDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <AgentDetailInner />
    </React.Suspense>
  );
}
