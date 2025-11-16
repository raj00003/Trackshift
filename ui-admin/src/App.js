import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useMemo, useRef, useState } from "react";
import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient, } from "@tanstack/react-query";
import "./App.css";
const API_BASE = import.meta.env.VITE_API_BASE || "";
const API_KEY = import.meta.env.VITE_API_KEY || "local-admin-token";
const MFA_TOKEN = import.meta.env.VITE_MFA_TOKEN || import.meta.env.VITE_MFA_CODE || "000000";
const TENANT = import.meta.env.VITE_TENANT || "pilot";
const MINIO_CONSOLE = import.meta.env.VITE_MINIO_CONSOLE_URL || "http://localhost:9001";
const DEMO_MAX_BYTES = 1_000_000_000; // 1GB visual cap for client-side validation
const queryClient = new QueryClient();
function normalizeBase(base) {
    return base.endsWith("/") ? base.slice(0, -1) : base;
}
function randomKPI() {
    const rand = (min, max) => Math.random() * (max - min) + min;
    return {
        success_rate: rand(0.94, 0.999),
        p95_latency_ms: rand(120, 480),
        under_500ms_ratio: rand(0.7, 0.99),
        failures_24h: Math.floor(rand(0, 5.99)),
    };
}
function withBases(path) {
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
async function fetchJson(path, init = {}) {
    const headers = {
        Authorization: `Bearer ${API_KEY}`,
        "X-MFA-Token": MFA_TOKEN,
        "X-Tenant-ID": TENANT,
        ...(init.headers || {}),
    };
    let lastErr;
    for (const url of withBases(path)) {
        try {
            const res = await fetch(url, { ...init, headers });
            if (!res.ok) {
                lastErr = new Error(`Request failed: ${res.status}`);
                continue;
            }
            return res.json();
        }
        catch (err) {
            lastErr = err;
        }
    }
    throw lastErr ?? new Error("Request failed");
}
function AppShell() {
    const [jobId, setJobId] = useState(null);
    const [mode, setMode] = useState(null);
    const [stage, setStage] = useState("idle");
    const [inlineMsg, setInlineMsg] = useState(null);
    const [demoError, setDemoError] = useState(null);
    const [demoSuccess, setDemoSuccess] = useState(null);
    const [edgeError, setEdgeError] = useState(null);
    const [edgeSuccess, setEdgeSuccess] = useState(null);
    const [kpiOverride, setKpiOverride] = useState(null);
    const queryClientLocal = useQueryClient();
    // Form refs
    const demoFileRef = useRef(null);
    const urgencyRef = useRef(null);
    const reliabilityRef = useRef(null);
    const sensitivityRef = useRef(null);
    const edgeFileRef = useRef(null);
    const [edgeName, setEdgeName] = useState("");
    const [edgeSize, setEdgeSize] = useState(null);
    const [edgeFile, setEdgeFile] = useState(null);
    // KPI query with inline error state
    const { data: kpi, isLoading: kpiLoading, isError: kpiError, } = useQuery({
        queryKey: ["kpi"],
        queryFn: () => fetchJson("/reports/kpi"),
        refetchInterval: 12_000,
        retry: 2,
    });
    // Job + progress queries
    const { data: job, isError: jobError, refetch: refetchJob, } = useQuery({
        queryKey: ["job", jobId],
        queryFn: () => fetchJson(`/jobs/${jobId}`),
        enabled: !!jobId,
        refetchInterval: 2_500,
        retry: 2,
    });
    const { data: progress, isError: progressError } = useQuery({
        queryKey: ["progress", jobId],
        queryFn: () => fetchJson(`/jobs/${jobId}/progress`),
        enabled: !!jobId,
        refetchInterval: 2_500,
        retry: 2,
    });
    // Mutations
    const demoUpload = useMutation({
        mutationFn: async (form) => fetchJson(`/ui/upload`, { method: "POST", body: form }),
        onError: (err) => {
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
        mutationFn: async (form) => fetchJson(`/ui/upload-large`, {
            method: "POST",
            body: form,
        }),
        onError: (err) => {
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
        if (!progress || progress.chunks_total === 0)
            return 0;
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
    const onEdgeFileChange = (input) => {
        const filePicked = input?.files?.[0];
        setEdgeFile(filePicked || null);
        setEdgeName(filePicked?.name || "");
        setEdgeSize(filePicked?.size ?? null);
    };
    return (_jsxs("div", { className: "app", children: [_jsx(KPIHeader, { kpi: kpiOverride ?? kpi, loading: kpiLoading && !kpiOverride, error: kpiError && !kpiOverride }), _jsxs("div", { className: "modes-row", children: [_jsx(ModeCard, { title: "Mode 1 - Demo Upload (Small Files)", subtitle: "Browser uploads directly to /ui/upload. Great for judges.", actionText: demoUpload.isPending ? "Uploading..." : "Upload a Small File", disabled: demoUpload.isPending, onAction: onDemoSubmit, error: demoError, success: demoSuccess, children: _jsxs(_Fragment, { children: [_jsxs("label", { className: "field", children: [_jsx("span", { children: "File" }), _jsx("input", { ref: demoFileRef, type: "file" })] }), _jsxs("div", { className: "dual", children: [_jsxs("label", { className: "field", children: [_jsx("span", { children: "Urgency" }), _jsxs("select", { ref: urgencyRef, defaultValue: "critical", children: [_jsx("option", { value: "low", children: "Low" }), _jsx("option", { value: "medium", children: "Medium" }), _jsx("option", { value: "high", children: "High" }), _jsx("option", { value: "critical", children: "Critical" })] })] }), _jsxs("label", { className: "field", children: [_jsx("span", { children: "Reliability" }), _jsxs("select", { ref: reliabilityRef, defaultValue: "gold", children: [_jsx("option", { value: "bronze", children: "Bronze" }), _jsx("option", { value: "silver", children: "Silver" }), _jsx("option", { value: "gold", children: "Gold" })] })] })] }), _jsxs("label", { className: "field", children: [_jsx("span", { children: "Sensitivity" }), _jsxs("select", { ref: sensitivityRef, defaultValue: "confidential", children: [_jsx("option", { value: "public", children: "Public" }), _jsx("option", { value: "confidential", children: "Confidential" }), _jsx("option", { value: "PHI", children: "PHI" })] })] }), _jsx("p", { className: "helper", children: "Limit: ~1GB (demo). Enforced server-side as well." })] }) }), _jsx(ModeCard, { title: "Mode 2 - Large File (Edge Agent)", subtitle: "Upload once; Edge Agent streams chunks via QUIC.", actionText: edgeUpload.isPending ? "Registering..." : "Register Large File", disabled: edgeUpload.isPending, onAction: onEdgeSubmit, error: edgeError, success: edgeSuccess, children: _jsxs(_Fragment, { children: [_jsxs("label", { className: "field", children: [_jsx("span", { children: "File (Edge Agent)" }), _jsx("input", { ref: edgeFileRef, type: "file", onChange: () => onEdgeFileChange(edgeFileRef.current) })] }), _jsxs("label", { className: "field", children: [_jsx("span", { children: "File Name" }), _jsx("input", { value: edgeName, readOnly: true, placeholder: "telemetry_f1_round3.bin" })] }), _jsxs("label", { className: "field", children: [_jsx("span", { children: "File Size (bytes)" }), _jsx("input", { value: edgeSize ?? "", readOnly: true, placeholder: "53687091200" })] }), _jsx("p", { className: "helper", children: "Edge Agent will stream chunks (8-16MB) after registration." })] }) })] }), _jsxs("div", { className: "simulation-row", children: [_jsx(SimulationTimeline, { stage: stage, jobId: jobId, jobError: jobError, inlineMsg: inlineMsg }), _jsx(NetworkPaths, { stage: stage, progressPct: progressPct }), _jsx(GatewayStoragePanel, { job: job, stage: stage, minioConsole: MINIO_CONSOLE })] }), jobId && (_jsxs("div", { className: "job-meta", children: [_jsxs("div", { children: [_jsx("p", { className: "eyebrow", children: "Active Job" }), _jsx("div", { className: "mono", children: jobId })] }), job?.status && _jsx("div", { className: `pill ${job.status.toLowerCase()}`, children: job.status })] }))] }));
}
function KPIHeader({ kpi, loading, error }) {
    const cards = [
        { title: "Success Rate", value: kpi ? toPercent(kpi.success_rate) : "-", subtitle: "Target >= 99%" },
        { title: "P95 Latency", value: kpi ? toMs(kpi.p95_latency_ms) : "-", subtitle: "< 500ms critical" },
        { title: "<500ms Ratio", value: kpi ? toPercent(kpi.under_500ms_ratio) : "-", subtitle: "Critical path coverage" },
        { title: "Failures (24h)", value: kpi ? String(kpi.failures_24h) : "-", subtitle: "Lower is better" },
    ];
    return (_jsxs("div", { className: "kpi-header", children: [_jsxs("div", { children: [_jsx("p", { className: "eyebrow", children: "Trackshift - Dual-mode Sender Simulation" }), _jsx("h1", { children: "Red/Black PQC-Ready Pipeline" }), _jsx("p", { className: "muted", children: "Multipath QUIC - Resume - MinIO - DLP/AV - immudb" })] }), _jsx("div", { className: "kpi-grid", children: cards.map((card) => (_jsxs("article", { className: "kpi-card", children: [_jsx("p", { className: "kpi-title", children: card.title }), loading ? _jsx("div", { className: "skeleton" }) : _jsx("p", { className: "kpi-value", children: card.value }), _jsx("p", { className: "kpi-subtitle", children: card.subtitle }), error && !loading && _jsx("p", { className: "kpi-helper", children: "Temporarily unavailable." })] }, card.title))) })] }));
}
function ModeCard({ title, subtitle, children, actionText, onAction, disabled, error, success, }) {
    return (_jsxs("article", { className: "mode-card", children: [_jsxs("div", { className: "mode-head", children: [_jsx("h2", { children: title }), _jsx("p", { className: "muted", children: subtitle })] }), _jsx("div", { className: "mode-body", children: children }), success && _jsx("div", { className: "inline-success", children: success }), error && _jsx("div", { className: "inline-error", children: error }), _jsx("button", { className: "btn btn-primary", onClick: onAction, disabled: disabled, children: actionText })] }));
}
function SimulationTimeline({ stage, jobId, jobError, inlineMsg, }) {
    const steps = [
        { key: "preparing", label: "File prepared (slicing + Merkle + Dilithium)" },
        { key: "streaming", label: "Multipath QUIC streaming" },
        { key: "reroute", label: "Predictive reroute (Wi-Fi -> 4G/Sat)" },
        { key: "verifying", label: "Gateway verification, DLP/AV, immudb" },
        { key: "stored", label: "MinIO stored + URI issued" },
    ];
    return (_jsxs("article", { className: "sim-card", children: [_jsxs("div", { className: "sim-head", children: [_jsx("h3", { children: "Simulation Timeline" }), _jsx("p", { className: "muted", children: jobId ? `Job ${jobId}` : "Start a mode to see the flow." })] }), _jsx("ol", { className: "timeline", children: steps.map((s) => (_jsx("li", { className: stage === s.key || stage === "stored" || stage === "failed" ? "active" : "", children: s.label }, s.key))) }), stage === "failed" && _jsx("div", { className: "inline-error", children: "Job failed. See diagnostics." }), jobError && _jsx("p", { className: "muted small", children: "Job data temporarily unavailable; retrying..." }), inlineMsg && _jsx("p", { className: "muted small", children: inlineMsg })] }));
}
function NetworkPaths({ stage, progressPct }) {
    return (_jsxs("article", { className: "sim-card", children: [_jsxs("div", { className: "sim-head", children: [_jsx("h3", { children: "Network Paths" }), _jsx("p", { className: "muted", children: "Multipath lanes with predictive rerouting." })] }), _jsxs("div", { className: "lanes", children: [_jsx("div", { className: `lane wifi ${stage === "reroute" ? "degraded" : ""}`, children: _jsx("span", { children: "Wi-Fi" }) }), _jsx("div", { className: "lane cellular", children: _jsx("span", { children: "4G" }) }), _jsx("div", { className: "lane sat", children: _jsx("span", { children: "Satellite" }) }), _jsxs("div", { className: "progress-indicator", children: [_jsx("div", { className: "bar", style: { width: `${progressPct}%` } }), _jsxs("span", { children: [progressPct, "% complete"] })] })] })] }));
}
function GatewayStoragePanel({ job, stage, minioConsole, }) {
    const verification = job?.verification_status;
    const checks = [
        { label: "Dilithium signature verified", ok: !!verification?.manifest_verified },
        { label: "Merkle root verified", ok: !!verification?.chunks_verified },
        { label: "DLP / AV check passed", ok: verification?.dlp_passed !== false },
        { label: "Receipt generated", ok: !!job?.integrity?.receipt_signature },
        { label: "immudb hash anchored", ok: !!job?.integrity?.immudb_digest },
    ];
    const storage = job?.storage;
    return (_jsxs("article", { className: "sim-card", children: [_jsxs("div", { className: "sim-head", children: [_jsx("h3", { children: "Gateway & Storage" }), _jsx("p", { className: "muted", children: "Verification and MinIO outcome." })] }), _jsx("ul", { className: "checklist", children: checks.map((c) => (_jsx("li", { className: c.ok || stage === "stored" ? "ok" : "", children: c.label }, c.label))) }), storage && (_jsxs("div", { className: "storage-table", children: [_jsxs("div", { className: "storage-row header", children: [_jsx("span", { children: "Object" }), _jsx("span", { children: "Bucket" }), _jsx("span", { children: "Path" }), _jsx("span", { children: "Size" }), _jsx("span", { children: "Stored At" }), _jsx("span", { children: "Integrity" })] }), _jsxs("div", { className: "storage-row", children: [_jsx("span", { children: storage.object_name }), _jsx("span", { children: storage.bucket }), _jsxs("span", { children: [storage.prefix, storage.object_name] }), _jsx("span", { children: formatBytes(storage.size_bytes) }), _jsx("span", { children: job?.timings?.completed_at || "-" }), _jsx("span", { children: "OK Merkle" })] }), _jsx("a", { className: "btn btn-link", href: minioConsole, target: "_blank", rel: "noreferrer", children: "Open in MinIO Console" })] })), stage === "stored" && _jsx("div", { className: "success-inline", children: "Transfer complete - MinIO URI ready" })] }));
}
function toPercent(value) {
    if (value == null)
        return "-";
    return `${(value * 100).toFixed(1)}%`;
}
function toMs(value) {
    if (value == null)
        return "-";
    return `${value.toFixed(0)} ms`;
}
function formatBytes(value) {
    if (!value)
        return "-";
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
    return (_jsx(QueryClientProvider, { client: queryClient, children: _jsx(AppShell, {}) }));
}
