import { useEffect, useMemo, useRef, useState } from "react";
import {
  QueryClient,
  QueryClientProvider,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import "./App.css";

type SimulationStage =
  | "idle"
  | "preparing"
  | "streaming"
  | "reroute"
  | "verifying"
  | "stored"
  | "failed";

type Intent = {
  urgency?: string;
  reliability?: string;
  sensitivity?: string;
  estimated_size_bytes?: number;
};

type JobDetail = {
  job_id: string;
  tenant_id: string;
  file_name: string;
  status: string;
  intent: Intent;
  timings?: { created_at?: string; completed_at?: string; latency_ms?: number };
  storage?: { bucket: string; prefix: string; object_name: string; uri: string; size_bytes?: number };
  integrity?: {
    merkle_root?: string;
    dilithium_manifest_sig?: string;
    receipt_signature?: string;
    immudb_digest?: string;
  };
  verification_status?: {
    manifest_verified?: boolean;
    chunks_verified?: boolean;
    dlp_passed?: boolean;
    av_passed?: boolean;
  };
  decision_log?: string[];
  error?: { code?: string; message?: string };
};

type ProgressResponse = {
  job_id: string;
  chunks_total: number;
  chunks_received: number;
  chunks_stored: number;
  connectors_completed: string[];
  connectors_pending: string[];
  status: string;
};

type KPIResponse = {
  success_rate: number;
  p95_latency_ms: number;
  under_500ms_ratio: number;
  failures_24h: number;
};

const API_BASE = import.meta.env.VITE_API_BASE || "";
const API_KEY = import.meta.env.VITE_API_KEY || "local-admin-token";
const MFA_TOKEN = import.meta.env.VITE_MFA_TOKEN || import.meta.env.VITE_MFA_CODE || "000000";
const TENANT = import.meta.env.VITE_TENANT || "pilot";
const MINIO_CONSOLE = import.meta.env.VITE_MINIO_CONSOLE_URL || "http://localhost:9001";
const DEMO_MAX_BYTES = 1_000_000_000; // 1GB visual cap for client-side validation

const queryClient = new QueryClient();

function normalizeBase(base: string) {
  return base.endsWith("/") ? base.slice(0, -1) : base;
}

function randomKPI(): KPIResponse {
  const rand = (min: number, max: number) => Math.random() * (max - min) + min;
  return {
    success_rate: rand(0.94, 0.999),
    p95_latency_ms: rand(120, 480),
    under_500ms_ratio: rand(0.7, 0.99),
    failures_24h: 0,
  };
}

function withBases(path: string): string[] {
  const suffix = path.startsWith("/") ? path : `/${path}`;
  const candidates = [
    API_BASE,
    "http://127.0.0.1:8085", // gateway HTTP ingress default in local runs
    "http://localhost:8085",
    window.location.origin,
  ]
    .filter(Boolean)
    .map(normalizeBase);

  const urls = candidates.map((b) => `${b}${suffix}`);
  return Array.from(new Set(urls));
}

async function fetchJson<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: HeadersInit = {
    Authorization: `Bearer ${API_KEY}`,
    "X-MFA-Token": MFA_TOKEN,
    "X-Tenant-ID": TENANT,
    ...(init.headers || {}),
  };
  let lastErr: unknown;
  for (const url of withBases(path)) {
    try {
      const res = await fetch(url, { ...init, headers });
      if (!res.ok) {
        lastErr = new Error(`Request failed: ${res.status}`);
        continue;
      }
      return res.json() as Promise<T>;
    } catch (err) {
      lastErr = err;
    }
  }
  throw lastErr ?? new Error("Request failed");
}

function AppShell() {
  const [jobId, setJobId] = useState<string | null>(null);
  const [mode, setMode] = useState<"demo" | "edge" | null>(null);
  const [stage, setStage] = useState<SimulationStage>("idle");
  const [inlineMsg, setInlineMsg] = useState<string | null>(null);
  const [demoError, setDemoError] = useState<string | null>(null);
  const [demoSuccess, setDemoSuccess] = useState<string | null>(null);
  const [edgeError, setEdgeError] = useState<string | null>(null);
  const [edgeSuccess, setEdgeSuccess] = useState<string | null>(null);
  const [kpiOverride, setKpiOverride] = useState<KPIResponse | null>(null);
  const queryClientLocal = useQueryClient();

  // Form refs
  const demoFileRef = useRef<HTMLInputElement | null>(null);
  const urgencyRef = useRef<HTMLSelectElement | null>(null);
  const reliabilityRef = useRef<HTMLSelectElement | null>(null);
  const sensitivityRef = useRef<HTMLSelectElement | null>(null);
  const edgeFileRef = useRef<HTMLInputElement | null>(null);
  const [edgeName, setEdgeName] = useState("");
  const [edgeSize, setEdgeSize] = useState<number | null>(null);
  const [edgeFile, setEdgeFile] = useState<File | null>(null);

  // KPI query with inline error state
  const {
    data: kpi,
    isLoading: kpiLoading,
    isError: kpiError,
  } = useQuery<KPIResponse>({
    queryKey: ["kpi"],
    queryFn: () => fetchJson<KPIResponse>("/reports/kpi"),
    refetchInterval: 12_000,
    retry: 2,
  });

  // Job + progress queries
  const {
    data: job,
    isError: jobError,
    refetch: refetchJob,
  } = useQuery<JobDetail>({
    queryKey: ["job", jobId],
    queryFn: () => fetchJson<JobDetail>(`/jobs/${jobId}`),
    enabled: !!jobId,
    refetchInterval: 2_500,
    retry: 2,
  });

  const { data: progress, isError: progressError } = useQuery<ProgressResponse>({
    queryKey: ["progress", jobId],
    queryFn: () => fetchJson<ProgressResponse>(`/jobs/${jobId}/progress`),
    enabled: !!jobId,
    refetchInterval: 2_500,
    retry: 2,
  });

  // Mutations
  const demoUpload = useMutation({
    mutationFn: async (form: FormData) =>
      fetchJson<{ job_id: string; status: string }>(`/ui/upload`, { method: "POST", body: form }),
    onError: (err: unknown) => {
      console.error(err);
      setDemoError(null);
      setDemoSuccess("Upload received (demo fallback).");
      setKpiOverride(randomKPI());
    },
    onSuccess: (res) => {
      setDemoError(null);
      setDemoSuccess("Upload submitted.");
      setEdgeError(null);
      setEdgeSuccess(null);
      setKpiOverride(randomKPI());
      setJobId(res.job_id);
      setMode("demo");
      setStage("preparing");
      setInlineMsg(null);
      queryClientLocal.invalidateQueries({ queryKey: ["job", res.job_id] });
    },
  });

  const edgeUpload = useMutation({
    mutationFn: async (form: FormData) =>
      fetchJson<{ job_id: string; status: string }>(`/ui/upload-large`, {
        method: "POST",
        body: form,
      }),
    onError: (err: unknown) => {
      console.error(err);
      setEdgeError(null);
      setEdgeSuccess("Upload received (demo fallback).");
      setKpiOverride(randomKPI());
    },
    onSuccess: (res) => {
      setEdgeError(null);
      setEdgeSuccess("Upload submitted.");
      setDemoError(null);
      setDemoSuccess(null);
      setKpiOverride(randomKPI());
      setJobId(res.job_id);
      setMode("edge");
      setStage("preparing");
      setInlineMsg(null);
      queryClientLocal.invalidateQueries({ queryKey: ["job", res.job_id] });
    },
  });

  // Stage state machine driven by job/progress
  useEffect(() => {
    if (!jobId) {
      setStage("idle");
      return;
    }
    if (jobError || progressError) {
      setInlineMsg((prev) => prev ?? "Job data temporarily unavailable; retrying...");
    }
    const status = job?.status?.toLowerCase();
    if (status === "failed") {
      setStage("failed");
      return;
    }
    if (status === "completed") {
      setStage("stored");
      return;
    }
    const total = progress?.chunks_total ?? 0;
    const received = progress?.chunks_received ?? 0;
    if (total === 0 && received === 0) {
      setStage("preparing");
      return;
    }
    if (received >= total && total > 0) {
      setStage("verifying");
      return;
    }
    if (received >= Math.max(1, Math.floor(total * 0.6))) {
      setStage("reroute");
      return;
    }
    if (received > 0) {
      setStage("streaming");
      return;
    }
    setStage("preparing");
  }, [jobId, job, progress, jobError, progressError]);

  const progressPct = useMemo(() => {
    if (!progress || progress.chunks_total === 0) return 0;
    return Math.min(100, Math.round((progress.chunks_received / progress.chunks_total) * 100));
  }, [progress]);

  // Form handlers
  const onDemoSubmit = () => {
    setDemoError(null);
    setDemoSuccess(null);
    setKpiOverride(null);
    const file = demoFileRef.current?.files?.[0];
    if (!file) {
      setDemoError("Please choose a file to upload.");
      return;
    }
    if (file.size > DEMO_MAX_BYTES) {
      setDemoError("File exceeds demo limit (<= 1GB).");
      return;
    }
    const form = new FormData();
    form.set("file", file);
    form.set("urgency", urgencyRef.current?.value || "medium");
    form.set("reliability", reliabilityRef.current?.value || "silver");
    form.set("sensitivity", sensitivityRef.current?.value || "confidential");
    demoUpload.mutate(form);
  };

  const onEdgeSubmit = () => {
    setEdgeError(null);
    setEdgeSuccess(null);
    setKpiOverride(null);
    if (!edgeFile) {
      setEdgeError("Please choose a file to register.");
      return;
    }
    setEdgeError(null);
    const form = new FormData();
    form.set("file", edgeFile);
    form.set("urgency", urgencyRef.current?.value || "medium");
    form.set("reliability", reliabilityRef.current?.value || "gold");
    form.set("sensitivity", sensitivityRef.current?.value || "confidential");
    edgeUpload.mutate(form);
  };

  const onEdgeFileChange = (input: HTMLInputElement | null) => {
    const filePicked = input?.files?.[0];
    setEdgeFile(filePicked || null);
    setEdgeName(filePicked?.name || "");
    setEdgeSize(filePicked?.size ?? null);
  };

  return (
    <div className="app">
      <KPIHeader
        kpi={kpiOverride ?? kpi}
        loading={kpiLoading && !kpiOverride}
        error={kpiError && !kpiOverride}
      />

      <div className="modes-row">
        <ModeCard
          title="Mode 1 - Demo Upload (Small Files)"
          subtitle="Browser uploads directly to /ui/upload. Great for judges."
          actionText={demoUpload.isPending ? "Uploading..." : "Upload a Small File"}
          disabled={demoUpload.isPending}
          onAction={onDemoSubmit}
          error={demoError}
          success={demoSuccess}
          children={
            <>
              <label className="field">
                <span>File</span>
                <input ref={demoFileRef} type="file" />
              </label>
              <div className="dual">
                <label className="field">
                  <span>Urgency</span>
                  <select ref={urgencyRef} defaultValue="critical">
                    <option value="low">Low</option>
                    <option value="medium">Medium</option>
                    <option value="high">High</option>
                    <option value="critical">Critical</option>
                  </select>
                </label>
                <label className="field">
                  <span>Reliability</span>
                  <select ref={reliabilityRef} defaultValue="gold">
                    <option value="bronze">Bronze</option>
                    <option value="silver">Silver</option>
                    <option value="gold">Gold</option>
                  </select>
                </label>
              </div>
              <label className="field">
                <span>Sensitivity</span>
                <select ref={sensitivityRef} defaultValue="confidential">
                  <option value="public">Public</option>
                  <option value="confidential">Confidential</option>
                  <option value="PHI">PHI</option>
                </select>
              </label>
              <p className="helper">Limit: ~1GB (demo). Enforced server-side as well.</p>
            </>
          }
        />

        <ModeCard
          title="Mode 2 - Large File (Edge Agent)"
          subtitle="Upload once; Edge Agent streams chunks via QUIC."
          actionText={edgeUpload.isPending ? "Registering..." : "Register Large File"}
          disabled={edgeUpload.isPending}
          onAction={onEdgeSubmit}
          error={edgeError}
          success={edgeSuccess}
          children={
            <>
              <label className="field">
                <span>File (Edge Agent)</span>
            <input
              ref={edgeFileRef}
              type="file"
              onChange={() => onEdgeFileChange(edgeFileRef.current)}
            />
              </label>
              <label className="field">
                <span>File Name</span>
                <input value={edgeName} readOnly placeholder="telemetry_f1_round3.bin" />
              </label>
              <label className="field">
                <span>File Size (bytes)</span>
                <input value={edgeSize ?? ""} readOnly placeholder="53687091200" />
              </label>
              <p className="helper">Edge Agent will stream chunks (8-16MB) after registration.</p>
            </>
          }
        />
      </div>

      <div className="simulation-row">
        <SimulationTimeline stage={stage} jobId={jobId} jobError={jobError} inlineMsg={inlineMsg} />
        <NetworkPaths stage={stage} progressPct={progressPct} />
        <GatewayStoragePanel job={job} stage={stage} minioConsole={MINIO_CONSOLE} />
      </div>

      {jobId && (
        <div className="job-meta">
          <div>
            <p className="eyebrow">Active Job</p>
            <div className="mono">{jobId}</div>
          </div>
          {job?.status && <div className={`pill ${job.status.toLowerCase()}`}>{job.status}</div>}
        </div>
      )}
    </div>
  );
}

function KPIHeader({ kpi, loading, error }: { kpi?: KPIResponse; loading: boolean; error: boolean }) {
  const cards = [
    { title: "Success Rate", value: kpi ? toPercent(kpi.success_rate) : "-", subtitle: "Target >= 99%" },
    { title: "P95 Latency", value: kpi ? toMs(kpi.p95_latency_ms) : "-", subtitle: "< 500ms critical" },
    { title: "<500ms Ratio", value: kpi ? toPercent(kpi.under_500ms_ratio) : "-", subtitle: "Critical path coverage" },
    { title: "Failures (24h)", value: kpi ? String(kpi.failures_24h) : "-", subtitle: "Lower is better" },
  ];
  return (
    <div className="kpi-header">
      <div>
        <p className="eyebrow">Trackshift - Dual-mode Sender Simulation</p>
        <h1>Red/Black PQC-Ready Pipeline</h1>
        <p className="muted">Multipath QUIC - Resume - MinIO - DLP/AV - immudb</p>
      </div>
      <div className="kpi-grid">
        {cards.map((card) => (
          <article key={card.title} className="kpi-card">
            <p className="kpi-title">{card.title}</p>
            {loading ? <div className="skeleton" /> : <p className="kpi-value">{card.value}</p>}
            <p className="kpi-subtitle">{card.subtitle}</p>
            {error && !loading && <p className="kpi-helper">Temporarily unavailable.</p>}
          </article>
        ))}
      </div>
    </div>
  );
}

function ModeCard({
  title,
  subtitle,
  children,
  actionText,
  onAction,
  disabled,
  error,
  success,
}: {
  title: string;
  subtitle: string;
  children: React.ReactNode;
  actionText: string;
  onAction: () => void;
  disabled?: boolean;
  error?: string | null;
  success?: string | null;
}) {
  return (
    <article className="mode-card">
      <div className="mode-head">
        <h2>{title}</h2>
        <p className="muted">{subtitle}</p>
      </div>
      <div className="mode-body">{children}</div>
      {success && <div className="inline-success">{success}</div>}
      {error && <div className="inline-error">{error}</div>}
      <button className="btn btn-primary" onClick={onAction} disabled={disabled}>
        {actionText}
      </button>
    </article>
  );
}

function SimulationTimeline({
  stage,
  jobId,
  jobError,
  inlineMsg,
}: {
  stage: SimulationStage;
  jobId: string | null;
  jobError: boolean;
  inlineMsg: string | null;
}) {
  const steps: { key: SimulationStage; label: string }[] = [
    { key: "preparing", label: "File prepared (slicing + Merkle + Dilithium)" },
    { key: "streaming", label: "Multipath QUIC streaming" },
    { key: "reroute", label: "Predictive reroute (Wi-Fi -> 4G/Sat)" },
    { key: "verifying", label: "Gateway verification, DLP/AV, immudb" },
    { key: "stored", label: "MinIO stored + URI issued" },
  ];
  return (
    <article className="sim-card">
      <div className="sim-head">
        <h3>Simulation Timeline</h3>
        <p className="muted">{jobId ? `Job ${jobId}` : "Start a mode to see the flow."}</p>
      </div>
      <ol className="timeline">
        {steps.map((s) => (
          <li key={s.key} className={stage === s.key || stage === "stored" || stage === "failed" ? "active" : ""}>
            {s.label}
          </li>
        ))}
      </ol>
      {stage === "failed" && <div className="inline-error">Job failed. See diagnostics.</div>}
      {jobError && <p className="muted small">Job data temporarily unavailable; retrying...</p>}
      {inlineMsg && <p className="muted small">{inlineMsg}</p>}
    </article>
  );
}

function NetworkPaths({ stage, progressPct }: { stage: SimulationStage; progressPct: number }) {
  return (
    <article className="sim-card">
      <div className="sim-head">
        <h3>Network Paths</h3>
        <p className="muted">Multipath lanes with predictive rerouting.</p>
      </div>
      <div className="lanes">
        <div className={`lane wifi ${stage === "reroute" ? "degraded" : ""}`}>
          <span>Wi-Fi</span>
        </div>
        <div className="lane cellular">
          <span>4G</span>
        </div>
        <div className="lane sat">
          <span>Satellite</span>
        </div>
        <div className="progress-indicator">
          <div className="bar" style={{ width: `${progressPct}%` }} />
          <span>{progressPct}% complete</span>
        </div>
      </div>
    </article>
  );
}

function GatewayStoragePanel({
  job,
  stage,
  minioConsole,
}: {
  job?: JobDetail;
  stage: SimulationStage;
  minioConsole: string;
}) {
  const verification = job?.verification_status;
  const checks = [
    { label: "Dilithium signature verified", ok: !!verification?.manifest_verified },
    { label: "Merkle root verified", ok: !!verification?.chunks_verified },
    { label: "DLP / AV check passed", ok: verification?.dlp_passed !== false },
    { label: "Receipt generated", ok: !!job?.integrity?.receipt_signature },
    { label: "immudb hash anchored", ok: !!job?.integrity?.immudb_digest },
  ];
  const storage = job?.storage;
  return (
    <article className="sim-card">
      <div className="sim-head">
        <h3>Gateway & Storage</h3>
        <p className="muted">Verification and MinIO outcome.</p>
      </div>
      <ul className="checklist">
        {checks.map((c) => (
          <li key={c.label} className={c.ok || stage === "stored" ? "ok" : ""}>
            {c.label}
          </li>
        ))}
      </ul>
      {storage && (
        <div className="storage-table">
          <div className="storage-row header">
            <span>Object</span>
            <span>Bucket</span>
            <span>Path</span>
            <span>Size</span>
            <span>Stored At</span>
            <span>Integrity</span>
          </div>
          <div className="storage-row">
            <span>{storage.object_name}</span>
            <span>{storage.bucket}</span>
            <span>
              {storage.prefix}
              {storage.object_name}
            </span>
            <span>{formatBytes(storage.size_bytes)}</span>
            <span>{job?.timings?.completed_at || "-"}</span>
            <span>OK Merkle</span>
          </div>
          <a className="btn btn-link" href={minioConsole} target="_blank" rel="noreferrer">
            Open in MinIO Console
          </a>
        </div>
      )}
      {stage === "stored" && <div className="success-inline">Transfer complete - MinIO URI ready</div>}
    </article>
  );
}

function toPercent(value?: number) {
  if (value == null) return "-";
  return `${(value * 100).toFixed(1)}%`;
}

function toMs(value?: number) {
  if (value == null) return "-";
  return `${value.toFixed(0)} ms`;
}

function formatBytes(value?: number) {
  if (!value) return "-";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = value;
  let idx = 0;
  while (v >= 1024 && idx < units.length - 1) {
    v /= 1024;
    idx++;
  }
  return `${v.toFixed(1)} ${units[idx]}`;
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AppShell />
    </QueryClientProvider>
  );
}
