"use client";

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import {
  ArrowLeftIcon,
  BookOpenIcon,
  CircleAlertIcon,
  DownloadIcon,
  ExternalLinkIcon,
  FileTextIcon,
  InfoIcon,
  Loader2Icon,
  PlusIcon,
  RefreshCwIcon,
  SaveIcon,
  SparklesIcon,
} from "lucide-react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { SubmissionMarkdown } from "@/components/submission-markdown";
import { emptySubmissionProfile, SubmissionProfileDialog } from "@/components/submission-profile-dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import {
  downloadSubmission,
  draftChecks,
  type SubmissionDraft,
  type SubmissionProfile,
  type SubmissionView,
  submissions,
} from "@/lib/submissions";
import type { Finding, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

const selectClass = "h-9 w-full min-w-0 rounded-md border bg-background px-2 text-sm";
const errorMessage = (e: unknown) => (e instanceof Error ? e.message : "操作失败，请重试");

function SubmissionPageInner() {
  const params = useSearchParams();
  const idsKey = [...new Set((params.get("ids") ?? "").split(",").filter((id) => /^[1-9]\d*$/.test(id)))]
    .slice(0, 20)
    .join(",");
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [profiles, setProfiles] = React.useState<SubmissionProfile[]>([]);
  const [models, setModels] = React.useState<LLMProfile[]>([]);
  const [findingId, setFindingId] = React.useState("");
  const [profileId, setProfileId] = React.useState("");
  const [view, setView] = React.useState<SubmissionView | null>(null);
  const [busy, setBusy] = React.useState("");
  const [initializing, setInitializing] = React.useState(true);
  const [error, setError] = React.useState("");
  const [dirty, setDirty] = React.useState(false);
  const [reviewed, setReviewed] = React.useState(false);
  const [tab, setTab] = React.useState("preview");
  const [editingProfile, setEditingProfile] = React.useState<SubmissionProfile | null>(null);
  const [dialog, setDialog] = React.useState<"generate" | "confirm" | "reload" | "reset" | null>(null);
  const [modelId, setModelId] = React.useState("0");
  const [allowModel, setAllowModel] = React.useState(false);
  const requestSequence = React.useRef(0);
  const busyRef = React.useRef(false);
  const profile = profiles.find((p) => p.id === profileId);
  const finding = findings.find((f) => f.id === findingId);
  const draft = view?.draft;
  const checks = view ? draftChecks(view) : [];
  const blocking = checks.filter((c) => c.blocking);

  const applyView = React.useCallback((next: SubmissionView) => {
    setView(next);
    setDirty(false);
    setReviewed(false);
    setError("");
  }, []);

  React.useEffect(() => {
    let live = true;
    setInitializing(true);
    const ids = idsKey ? idsKey.split(",") : [];
    void Promise.all([
      Promise.all(ids.map((id) => api.getFinding(id))),
      submissions.profiles(),
      api.llmProfiles().catch(() => [] as LLMProfile[]),
    ])
      .then(([items, programs, llms]) => {
        if (!live) return;
        setFindings(items);
        setProfiles(programs);
        setModels(llms);
        setFindingId(items[0]?.id ?? "");
        setProfileId(programs[0]?.id ?? "");
      })
      .catch((e) => {
        if (live) setError(errorMessage(e));
      })
      .finally(() => {
        if (live) setInitializing(false);
      });
    return () => {
      live = false;
    };
  }, [idsKey]);

  React.useEffect(() => {
    if (!findingId || !profileId) {
      setView(null);
      return;
    }
    const sequence = ++requestSequence.current;
    setView(null);
    setBusy("载入草稿");
    busyRef.current = true;
    void submissions
      .prepare(findingId, profileId)
      .then((next) => {
        if (sequence === requestSequence.current) applyView(next);
      })
      .catch((e) => {
        if (sequence === requestSequence.current) setError(errorMessage(e));
      })
      .finally(() => {
        if (sequence === requestSequence.current) {
          setBusy("");
          busyRef.current = false;
        }
      });
    return () => {
      ++requestSequence.current;
    };
  }, [findingId, profileId, applyView]);

  React.useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (dirty || busyRef.current) e.preventDefault();
    };
    const guardLink = (e: MouseEvent) => {
      const link = e.target instanceof Element ? e.target.closest("a[href]") : null;
      if (
        !(link instanceof HTMLAnchorElement) ||
        link.target === "_blank" ||
        e.metaKey ||
        e.ctrlKey ||
        e.shiftKey ||
        e.altKey ||
        e.button !== 0
      )
        return;
      if (link.href === window.location.href || (!dirty && !busyRef.current)) return;
      if (
        !window.confirm(busyRef.current ? "操作尚未完成，确定离开此页面？" : "草稿有未保存的修改，确定离开此页面？")
      ) {
        e.preventDefault();
        e.stopPropagation();
      }
    };
    window.addEventListener("beforeunload", warn);
    document.addEventListener("click", guardLink, true);
    return () => {
      window.removeEventListener("beforeunload", warn);
      document.removeEventListener("click", guardLink, true);
    };
  }, [dirty]);

  const run = async (label: string, action: () => Promise<void>) => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(label);
    setError("");
    try {
      await action();
    } catch (e) {
      setError(errorMessage(e));
      toast.error(errorMessage(e));
    } finally {
      busyRef.current = false;
      setBusy("");
    }
  };
  const saveCurrent = async () => {
    if (!view) throw new Error("没有可保存的草稿");
    if (!dirty) return view;
    const saved = await submissions.save(view.draft);
    applyView(saved);
    return saved;
  };
  const navigate = (kind: "finding" | "profile", value: string) =>
    void run("保存草稿", async () => {
      if (view && dirty) await saveCurrent();
      setDialog(null);
      setReviewed(false);
      if (kind === "finding") setFindingId(value);
      else setProfileId(value);
    });
  const edit = (patch: Partial<SubmissionDraft>) => {
    if (!view || busyRef.current) return;
    setView({ ...view, draft: { ...view.draft, ...patch, state: "draft", reviewed_at: null } });
    setDirty(true);
    setReviewed(false);
  };
  const generate = () =>
    void run("LLM 正在整理报告", async () => {
      const saved = await saveCurrent();
      const next = await submissions.generate(saved.draft, Number(modelId));
      applyView(next);
      setDialog(null);
      setTab("preview");
      toast.success("草稿已生成，请核对事实与缺失材料");
    });
  const confirmAndExport = () =>
    void run("确认并导出", async () => {
      const saved = await saveCurrent();
      const confirmed = await submissions.confirm(saved.draft);
      applyView(confirmed);
      const file = await submissions.export(confirmed.draft);
      downloadSubmission(file.filename, file.markdown);
      setDialog(null);
      toast.success("材料已导出，尚未发送给厂商");
    });
  const savedProfile = (next: SubmissionProfile) => {
    setProfiles((items) => [next, ...items.filter((p) => p.id !== next.id)]);
    setEditingProfile(null);
    if (profileId !== next.id) {
      setProfileId(next.id);
      return;
    }
    if (view)
      void run("刷新规则检查", async () => {
        applyView(await submissions.load(view.draft.id));
      });
  };
  const openProfile = (next: SubmissionProfile) =>
    void run("保存草稿", async () => {
      if (view && dirty) await saveCurrent();
      setEditingProfile(next);
    });

  return (
    <div className="flex min-w-0 flex-1 flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b px-4 py-3 lg:px-6">
        <SidebarTrigger />
        <Button variant="ghost" size="icon" asChild>
          <Link href="/function/findings" aria-label="返回漏洞列表">
            <ArrowLeftIcon />
          </Link>
        </Button>
        <div className="min-w-0">
          <h1 className="font-semibold text-base">提交预览</h1>
          <p className="text-muted-foreground text-xs">核对厂商要求、报告内容与证据</p>
        </div>
        <Badge variant="outline" className="ml-auto">
          材料准备 · 人工投递
        </Badge>
      </header>
      <div className="space-y-4 p-4 lg:p-6">
        {error && (
          <Alert variant="destructive">
            <AlertDescription className="flex flex-wrap items-center gap-3">
              {error}
              {view && (
                <Button variant="outline" size="sm" disabled={!!busy} onClick={() => setDialog("reload")}>
                  重新载入
                </Button>
              )}
            </AlertDescription>
          </Alert>
        )}
        {initializing && (
          <p className="flex items-center gap-2 text-muted-foreground text-sm">
            <Loader2Icon className="size-4 animate-spin" />
            载入漏洞与厂商规则…
          </p>
        )}
        {!initializing && !findings.length && (
          <Card>
            <CardContent className="py-8 text-center text-muted-foreground text-sm">
              请先在漏洞列表勾选漏洞，或从漏洞详情进入提交预览。每次最多准备 20 条。
            </CardContent>
          </Card>
        )}
        {!!findings.length && (
          <div className="grid items-start gap-4 xl:grid-cols-[210px_minmax(0,1fr)_260px]">
            <aside className="space-y-4">
              <Card className="gap-3 py-4">
                <CardHeader className="px-4">
                  <CardTitle className="flex items-center justify-between text-sm">
                    待提交漏洞<span className="font-normal text-muted-foreground text-xs">{findings.length} 条</span>
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-2 px-2">
                  {findings.map((f) => (
                    <button
                      key={f.id}
                      type="button"
                      disabled={!!busy}
                      aria-pressed={f.id === findingId}
                      onClick={() => navigate("finding", f.id)}
                      className={cn(
                        "w-full rounded-md border border-transparent p-3 text-left disabled:opacity-50",
                        f.id === findingId ? "border-primary/20 bg-primary/5" : "hover:bg-muted",
                      )}
                    >
                      <span className="mb-2 flex items-center justify-between gap-2 text-muted-foreground text-xs">
                        <span>#{f.id}</span>
                        <StatusBadge domain="severity" value={f.severity} />
                      </span>
                      <span className="block font-medium text-sm">{f.name || f.vulnclass}</span>
                    </button>
                  ))}
                </CardContent>
              </Card>
              <Card className="gap-3 py-4">
                <CardHeader className="px-4">
                  <CardTitle className="text-sm">目标项目</CardTitle>
                </CardHeader>
                <CardContent className="space-y-3 px-4">
                  <select
                    aria-label="目标项目"
                    className={selectClass}
                    value={profileId}
                    disabled={!!busy}
                    onChange={(e) => navigate("profile", e.target.value)}
                  >
                    <option value="" disabled>
                      请选择厂商规则
                    </option>
                    {profiles.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </select>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={!!busy}
                      onClick={() => openProfile(emptySubmissionProfile())}
                    >
                      <PlusIcon />
                      新增
                    </Button>
                    {profile && (
                      <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => openProfile(profile)}>
                        编辑规则
                      </Button>
                    )}
                  </div>
                  {profile && (
                    <p className="text-muted-foreground text-xs">
                      {profile.channel === "hackerone" ? "HackerOne" : "厂商网页"} · 规则 v{profile.revision}
                    </p>
                  )}
                </CardContent>
              </Card>
            </aside>
            <main className="min-w-0">
              {!profile && (
                <Card>
                  <CardContent className="space-y-4 py-12 text-center">
                    <BookOpenIcon className="mx-auto size-7 text-muted-foreground" />
                    <p className="text-sm">先添加厂商的规则和报告模板</p>
                    <Button onClick={() => openProfile(emptySubmissionProfile())}>
                      <PlusIcon />
                      新增厂商规则
                    </Button>
                  </CardContent>
                </Card>
              )}
              {profile && !view && (
                <Card>
                  <CardContent className="flex items-center gap-2 py-12 text-muted-foreground text-sm">
                    <Loader2Icon className="size-4 animate-spin" />
                    {busy || "草稿暂不可用，请重新选择项目"}
                  </CardContent>
                </Card>
              )}
              {draft && (
                <Card className="gap-0 overflow-hidden py-0">
                  <div className="flex flex-wrap items-center gap-2 border-b p-4">
                    <FileTextIcon className="size-4 text-muted-foreground" />
                    <span className="font-medium text-sm">提交给厂商的内容</span>
                    <Badge variant="secondary">{dirty ? "未保存" : `草稿 v${draft.revision}`}</Badge>
                    <div className="ml-auto flex flex-wrap gap-2">
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={!!busy}
                        onClick={() =>
                          void run("刷新检查", async () => {
                            const saved = await saveCurrent();
                            applyView(await submissions.load(saved.draft.id));
                          })
                        }
                      >
                        <RefreshCwIcon />
                        刷新检查
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={!!busy || !dirty}
                        onClick={() =>
                          void run("保存草稿", async () => {
                            await saveCurrent();
                            toast.success("草稿已保存");
                          })
                        }
                      >
                        <SaveIcon />
                        保存
                      </Button>
                      <Button
                        size="sm"
                        disabled={!!busy}
                        onClick={() => {
                          setAllowModel(false);
                          setDialog("generate");
                        }}
                      >
                        <SparklesIcon />
                        智能适配
                      </Button>
                    </div>
                  </div>
                  <Tabs value={tab} onValueChange={setTab} className="gap-0">
                    <TabsList className="m-4 mb-0 flex-wrap">
                      <TabsTrigger value="preview">报告预览</TabsTrigger>
                      <TabsTrigger value="edit">编辑字段</TabsTrigger>
                      <TabsTrigger value="evidence">证据与脱敏</TabsTrigger>
                    </TabsList>
                    <TabsContent value="preview" className="space-y-6 p-5">
                      <div>
                        <p className="mb-2 text-muted-foreground text-xs">
                          {draft.profile.name} · #{draft.finding_id} · 模板 v{draft.profile.revision}
                        </p>
                        <h2 className="break-words font-semibold text-xl">{draft.title || "待填写报告标题"}</h2>
                        <div className="mt-3 flex flex-wrap gap-2">
                          <StatusBadge domain="severity" value={draft.source.severity} />
                          <Badge variant="outline">{draft.profile.language === "en" ? "English" : "简体中文"}</Badge>
                          {finding?.assets?.map((a) => (
                            <Badge key={a.id} variant="outline" className="max-w-full whitespace-normal break-all">
                              {a.label}
                            </Badge>
                          ))}
                        </div>
                      </div>
                      <SubmissionMarkdown text={draft.body} />
                      <section className="space-y-2 border-t pt-4">
                        <h3 className="font-semibold text-sm">实际安全影响 / Impact</h3>
                        {draft.impact.trim() ? (
                          <SubmissionMarkdown text={draft.impact} />
                        ) : (
                          <Missing label="实际安全影响" onEdit={() => setTab("edit")} />
                        )}
                      </section>
                      {draft.fields.map((field) => (
                        <section key={field.id} className="space-y-2 border-t pt-4">
                          <h3 className="flex flex-wrap items-center gap-2 font-medium text-sm">
                            {field.label}
                            {field.required && <Badge variant="outline">必填</Badge>}
                          </h3>
                          {field.value.trim() ? (
                            <SubmissionMarkdown text={field.value} />
                          ) : field.required ? (
                            <Missing label={field.label} onEdit={() => setTab("edit")} />
                          ) : (
                            <p className="text-muted-foreground text-xs">未填写</p>
                          )}
                          {field.sources.length > 0 && (
                            <p className="text-muted-foreground text-xs">来源：{field.sources.join(" / ")}</p>
                          )}
                        </section>
                      ))}
                      <p className="border-t pt-3 text-muted-foreground text-xs">
                        {draft.model ? `由 ${draft.model} 整理，需人工核对` : "原始材料草稿，可手动编辑或使用智能适配"}{" "}
                        · 预览不会加载正文中的外部图片或链接
                      </p>
                    </TabsContent>
                    <TabsContent value="edit" className="p-5">
                      <fieldset disabled={!!busy} className="space-y-5">
                        <label htmlFor="submission-title" className="block space-y-2 text-sm">
                          标题
                          <Input
                            id="submission-title"
                            value={draft.title}
                            onChange={(e) => edit({ title: e.target.value })}
                          />
                        </label>
                        <label htmlFor="submission-body" className="block space-y-2 text-sm">
                          报告正文 · Markdown
                          <Textarea
                            id="submission-body"
                            className="min-h-72 font-mono text-xs"
                            value={draft.body}
                            onChange={(e) => edit({ body: e.target.value })}
                          />
                        </label>
                        <label htmlFor="submission-impact" className="block space-y-2 text-sm">
                          实际安全影响 *
                          <Textarea
                            id="submission-impact"
                            className="min-h-28"
                            value={draft.impact}
                            onChange={(e) => edit({ impact: e.target.value })}
                            placeholder="仅填写证据已经验证的影响"
                          />
                        </label>
                        {draft.fields.map((field) => (
                          <label
                            key={field.id}
                            htmlFor={`submission-field-${field.id}`}
                            className="block space-y-2 text-sm"
                          >
                            {field.label}
                            {field.required ? " *" : ""}
                            <Textarea
                              id={`submission-field-${field.id}`}
                              value={field.value}
                              onChange={(e) =>
                                edit({
                                  fields: draft.fields.map((f) =>
                                    f.id === field.id ? { ...f, value: e.target.value, sources: ["manual"] } : f,
                                  ),
                                })
                              }
                            />
                            {field.instruction && (
                              <span className="block text-muted-foreground text-xs">{field.instruction}</span>
                            )}
                          </label>
                        ))}
                        <Button variant="outline" onClick={() => setTab("preview")}>
                          查看报告预览
                        </Button>
                      </fieldset>
                    </TabsContent>
                    <TabsContent value="evidence" className="space-y-4 p-5">
                      <Alert>
                        <AlertDescription>
                          这里是当前草稿使用的证据快照。已隐藏常见凭据格式，仍需人工检查个人信息与敏感内容。原始流量文件、截图和任务目录不会自动传给模型。
                        </AlertDescription>
                      </Alert>
                      {[
                        ["漏洞摘要", draft.source.summary],
                        ["证据 / PoC", draft.source.evidence],
                        ["详细报告", draft.source.report],
                      ].map(([label, text]) => (
                        <section key={label} className="space-y-2">
                          <h3 className="font-medium text-sm">{label}</h3>
                          <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted p-3 font-mono text-xs">
                            {text || "无"}
                          </pre>
                        </section>
                      ))}
                    </TabsContent>
                  </Tabs>
                </Card>
              )}
            </main>
            <aside className="space-y-4 xl:sticky xl:top-4">
              <Card className="gap-3 py-4">
                <CardHeader className="px-4">
                  <CardTitle className="flex items-center justify-between text-sm">
                    提交检查
                    <Badge variant={blocking.length ? "outline" : "secondary"}>
                      {view ? `${blocking.length} 项待处理` : "待准备"}
                    </Badge>
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-3 px-4">
                  {checks.map((check) => (
                    <div key={`${check.code}:${check.message}`} className="flex items-start gap-2 text-xs">
                      {check.blocking ? (
                        <CircleAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
                      ) : (
                        <InfoIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                      )}
                      <span>{check.message}</span>
                    </div>
                  ))}
                  {blocking.some((c) => c.code === "missing") && (
                    <Button variant="outline" size="sm" onClick={() => setTab("edit")}>
                      去补全材料
                    </Button>
                  )}
                  {findingId && (
                    <Button size="sm" variant="ghost" asChild>
                      <Link href={`/function/findings/detail?id=${findingId}`} target="_blank" rel="noreferrer">
                        查看原漏洞
                        <ExternalLinkIcon />
                      </Link>
                    </Button>
                  )}
                </CardContent>
              </Card>
              {profile && (
                <Card className="gap-3 py-4">
                  <CardHeader className="px-4">
                    <CardTitle className="flex items-center gap-2 text-sm">
                      <BookOpenIcon className="size-4" />
                      厂商要求
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="space-y-3 px-4">
                    <p className="max-h-72 overflow-auto whitespace-pre-wrap text-xs leading-relaxed">
                      {profile.rules}
                    </p>
                    {profile.source_url && (
                      <a
                        className="text-primary text-xs underline"
                        href={profile.source_url}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        查看规则来源
                      </a>
                    )}
                    <p className="text-muted-foreground text-xs">
                      规则需手动更新 · {profile.updated_at ? new Date(profile.updated_at).toLocaleString() : ""}
                    </p>
                  </CardContent>
                </Card>
              )}
              {blocking.some((c) => c.code === "source_changed" || c.code === "profile_changed") && (
                <Button variant="outline" disabled={!!busy} onClick={() => setDialog("reset")}>
                  <RefreshCwIcon />
                  按最新来源重建草稿
                </Button>
              )}
              {!!draft?.warnings.length && (
                <Alert>
                  <AlertDescription className="space-y-2">
                    <p className="font-medium">模型提示 · 需核实</p>
                    {draft.warnings.map((warning) => (
                      <p key={warning} className="text-xs">
                        {warning}
                      </p>
                    ))}
                  </AlertDescription>
                </Alert>
              )}
            </aside>
          </div>
        )}
        {draft && (
          <div className="flex flex-wrap items-center justify-between gap-4 rounded-lg border bg-card p-4">
            <div className="space-y-1">
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={reviewed}
                  disabled={!!busy}
                  onChange={(e) => setReviewed(e.target.checked)}
                />
                我已验证复现结果，并核对项目范围和报告内容
              </label>
              <p className="pl-5 text-muted-foreground text-xs">导出固定版本的材料；实际投递在厂商官方页面完成。</p>
            </div>
            <div className="flex flex-wrap items-center gap-3">
              {busy && (
                <span className="flex items-center gap-2 text-muted-foreground text-xs" role="status">
                  <Loader2Icon className="size-4 animate-spin" />
                  {busy}…
                </span>
              )}
              {draft.state === "ready" && !dirty && !blocking.length && (
                <Button variant="outline" asChild>
                  <a href={draft.profile.destination} target="_blank" rel="noopener noreferrer">
                    打开官方提交页
                    <ExternalLinkIcon />
                  </a>
                </Button>
              )}
              <Button disabled={!!busy || !reviewed || blocking.length > 0} onClick={() => setDialog("confirm")}>
                <DownloadIcon />
                确认并导出材料
              </Button>
            </div>
          </div>
        )}
      </div>
      {editingProfile && (
        <SubmissionProfileDialog
          initial={editingProfile}
          onClose={() => setEditingProfile(null)}
          onSaved={savedProfile}
        />
      )}
      <Dialog
        open={dialog === "generate"}
        onOpenChange={(open) => {
          if (!open && !busy) setDialog(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>按厂商要求整理报告</DialogTitle>
            <DialogDescription>
              将最新的漏洞摘要、证据、详细报告和厂商规则交给所选模型。生成结果会替换当前草稿；失败时保留原稿。
            </DialogDescription>
          </DialogHeader>
          <label className="space-y-2 text-sm">
            报告适配模型
            <select
              className={selectClass}
              value={modelId}
              disabled={!!busy}
              onChange={(e) => setModelId(e.target.value)}
            >
              <option value="0">当前全局模型</option>
              {models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name} · {m.model}
                </option>
              ))}
            </select>
          </label>
          <p className="text-muted-foreground text-xs">
            仅使用选定配置，不自动切换其他供应商。此操作会产生模型调用费用。
          </p>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={allowModel}
              disabled={!!busy}
              onChange={(e) => setAllowModel(e.target.checked)}
            />
            我确认该项目允许用所选模型处理这些材料
          </label>
          <DialogFooter>
            <Button variant="outline" disabled={!!busy} onClick={() => setDialog(null)}>
              取消
            </Button>
            <Button disabled={!!busy || !allowModel} onClick={generate}>
              {busy ? <Loader2Icon className="animate-spin" /> : <SparklesIcon />}
              {busy ? "正在生成…" : "生成并替换草稿"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={dialog === "confirm"}
        onOpenChange={(open) => {
          if (!open && !busy) setDialog(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>确认导出这份报告</DialogTitle>
            <DialogDescription>
              保存并确认当前内容后，下载 Markdown 材料。此操作不会自动向厂商提交报告。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2 rounded-md bg-muted p-3 text-sm">
            <p>{draft?.profile.name}</p>
            <p className="break-words font-medium">{draft?.title}</p>
            <p className="break-all text-muted-foreground text-xs">{draft?.profile.destination}</p>
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={!!busy} onClick={() => setDialog(null)}>
              返回检查
            </Button>
            <Button disabled={!!busy || !reviewed || !!blocking.length} onClick={confirmAndExport}>
              <DownloadIcon />
              {busy ? "导出中…" : "确认并下载"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={dialog === "reload" || dialog === "reset"}
        onOpenChange={(open) => {
          if (!open) setDialog(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{dialog === "reset" ? "按最新来源重建草稿" : "重新载入服务器草稿"}</DialogTitle>
            <DialogDescription>
              {dialog === "reset"
                ? "现有正文和字段将被最新漏洞材料与厂商模板替换，需要重新填写影响并确认。此操作不调用模型。"
                : "未保存的编辑将被替换为服务器上的最新版本。"}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialog(null)}>
              保留编辑
            </Button>
            <Button
              disabled={!!busy}
              onClick={() =>
                void run("载入最新草稿", async () => {
                  if (draft)
                    applyView(dialog === "reset" ? await submissions.reset(draft) : await submissions.load(draft.id));
                  setDialog(null);
                })
              }
            >
              <RefreshCwIcon />
              重新载入
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function Missing({ label, onEdit }: { label: string; onEdit: () => void }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-500/20 bg-amber-500/5 p-3 text-sm">
      <span>待补充：{label}</span>
      <Button variant="ghost" size="sm" onClick={onEdit}>
        编辑
      </Button>
    </div>
  );
}

export default function SubmissionPage() {
  return (
    <React.Suspense fallback={<p className="p-6 text-muted-foreground text-sm">载入提交预览…</p>}>
      <SubmissionPageInner />
    </React.Suspense>
  );
}
