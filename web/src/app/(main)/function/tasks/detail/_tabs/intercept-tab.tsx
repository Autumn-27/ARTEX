"use client";

import { ApprovalRecords } from "@/components/approval-records";

export function InterceptTab({ taskId }: { taskId: string }) {
  return <ApprovalRecords key={taskId} taskId={taskId} />;
}
