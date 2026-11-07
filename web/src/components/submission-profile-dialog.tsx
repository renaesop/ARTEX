"use client";

import { useState } from "react";

import { PlusIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { type SubmissionProfile, submissions } from "@/lib/submissions";

const selectClass = "h-9 w-full rounded-md border bg-background px-2 text-sm";
export const emptySubmissionProfile = (): SubmissionProfile => ({
  id: "",
  revision: 0,
  name: "",
  channel: "hackerone",
  destination: "",
  source_url: "",
  rules: "",
  language: "en",
  fields: [],
});

export function SubmissionProfileDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: SubmissionProfile;
  onClose: () => void;
  onSaved: (profile: SubmissionProfile) => void;
}) {
  const [profile, setProfile] = useState(initial);
  const [saving, setSaving] = useState(false);
  const update = (patch: Partial<SubmissionProfile>) => setProfile((p) => ({ ...p, ...patch }));
  const save = async () => {
    setSaving(true);
    try {
      const next = await submissions.saveProfile(profile);
      onSaved(next);
      toast.success("厂商规则已保存");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !saving) onClose();
      }}
    >
      <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{profile.id ? "编辑厂商规则" : "新增厂商规则"}</DialogTitle>
          <DialogDescription>粘贴官方项目规则和模板。保存来源链接供核对；此处不会自动抓取网页。</DialogDescription>
        </DialogHeader>
        <fieldset disabled={saving} className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <label htmlFor="submission-profile-name" className="space-y-2 text-sm">
              项目名称
              <Input
                id="submission-profile-name"
                value={profile.name}
                maxLength={100}
                onChange={(e) => update({ name: e.target.value })}
                placeholder="厂商 / 项目名称"
              />
            </label>
            <label className="space-y-2 text-sm">
              提交渠道
              <select
                className={selectClass}
                value={profile.channel}
                onChange={(e) => update({ channel: e.target.value as SubmissionProfile["channel"] })}
              >
                <option value="hackerone">HackerOne</option>
                <option value="web">厂商 SRC / 网页</option>
              </select>
            </label>
          </div>
          <label htmlFor="submission-profile-destination" className="block space-y-2 text-sm">
            官方提交入口
            <Input
              id="submission-profile-destination"
              type="url"
              value={profile.destination}
              onChange={(e) => update({ destination: e.target.value })}
              placeholder="https://hackerone.com/your-program"
            />
          </label>
          <label htmlFor="submission-profile-source" className="block space-y-2 text-sm">
            规则来源链接
            <Input
              id="submission-profile-source"
              type="url"
              value={profile.source_url}
              onChange={(e) => update({ source_url: e.target.value })}
              placeholder="https://…"
            />
          </label>
          <label htmlFor="submission-profile-rules" className="block space-y-2 text-sm">
            厂商规则与报告模板
            <Textarea
              id="submission-profile-rules"
              className="min-h-44"
              value={profile.rules}
              onChange={(e) => update({ rules: e.target.value })}
              placeholder="粘贴范围、漏洞类型要求、复现材料要求、报告模板和自定义字段说明。不要粘贴密码或 API Key。"
            />
          </label>
          <label className="block space-y-2 text-sm">
            报告语言
            <select
              className={selectClass}
              value={profile.language}
              onChange={(e) => update({ language: e.target.value as "en" | "zh" })}
            >
              <option value="en">English</option>
              <option value="zh">简体中文</option>
            </select>
          </label>
          <div className="space-y-3">
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium text-sm">固定字段</span>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={profile.fields.length >= 24}
                onClick={() =>
                  update({
                    fields: [
                      ...profile.fields,
                      {
                        id: `field_${crypto.randomUUID().slice(0, 8)}`,
                        label: "",
                        instruction: "",
                        value: "",
                        required: true,
                        sources: [],
                      },
                    ],
                  })
                }
              >
                <PlusIcon />
                添加字段
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              用于厂商明确要求的版本、账号角色等。LLM 还会根据规则提示其他缺失材料。
            </p>
            {profile.fields.map((field) => (
              <div key={field.id} className="space-y-2 rounded-md border p-3">
                <div className="flex gap-2">
                  <Input
                    aria-label="字段名称"
                    value={field.label}
                    onChange={(e) =>
                      update({
                        fields: profile.fields.map((f) => (f.id === field.id ? { ...f, label: e.target.value } : f)),
                      })
                    }
                    placeholder="字段名称，如受影响版本"
                  />
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label={`删除字段 ${field.label}`}
                    onClick={() => update({ fields: profile.fields.filter((f) => f.id !== field.id) })}
                  >
                    <Trash2Icon />
                  </Button>
                </div>
                <Input
                  aria-label="字段要求"
                  value={field.instruction}
                  onChange={(e) =>
                    update({
                      fields: profile.fields.map((f) =>
                        f.id === field.id ? { ...f, instruction: e.target.value } : f,
                      ),
                    })
                  }
                  placeholder="填写要求"
                />
                <label className="flex items-center gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={field.required}
                    onChange={(e) =>
                      update({
                        fields: profile.fields.map((f) =>
                          f.id === field.id ? { ...f, required: e.target.checked } : f,
                        ),
                      })
                    }
                  />
                  必填
                </label>
              </div>
            ))}
          </div>
        </fieldset>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button onClick={() => void save()} disabled={saving}>
            {saving ? "保存中…" : "保存规则"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
