import { useCallback, useEffect, useMemo, useState } from "react";
import "./App.css";

type Job = {
  id: string;
  tenantId: string;
  state: string;
  intent: Record<string, unknown>;
  prediction?: { path: string; strategy: string; latencyMs: number; failureGuard: string };
  policyDecisions?: { rule: string; action: string; reason: string }[];
  receipt?: { receiptId: string; signatureAlg: string };
  decisionLog?: string[];
  createdAt?: string;
};

type KPI = {
  successRate: number;
  p95LatencyMs: number;
  sub500Ratio: number;
  failures: number;
  canceled: number;
};

type Scenario = "f1" | "clinic";

// Default to a relative "/api" so Vite dev proxy can forward to the orchestrator without CORS.
const API_BASE = import.meta.env.VITE_API_BASE || "/api";
const API_KEY = import.meta.env.VITE_API_KEY || "local-admin-token";
const MFA_TOKEN = import.meta.env.VITE_MFA_TOKEN || "000000";
const TENANT = import.meta.env.VITE_TENANT || "pilot";

function withBase(base: string, path: string) {
  const normalizedBase = base.endsWith("/") ? base.slice(0, -1) : base;
  return `${normalizedBase}${path.startsWith("/") ? path : `/${path}`}`;
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: HeadersInit = {
    Authorization: `Bearer ${API_KEY}`,
    "X-MFA-Token": MFA_TOKEN,
    "X-Tenant-ID": TENANT,
  };
  if (init?.body) {
    headers["Content-Type"] = "application/json";
  }
  const bases = [API_BASE, window.location.origin].filter(Boolean);
  let lastError: unknown;
  for (const base of bases) {
    try {
      const response = await fetch(withBase(base, path), {
        ...init,
        headers: { ...headers, ...(init?.headers || {}) },
      });
      if (!response.ok) {
        lastError = new Error(`Request failed (${response.status})`);
        continue;
      }
      return (await response.json()) as T;
    } catch (err) {
      lastError = err;
    }
  }
  throw lastError ?? new Error("Request failed");
}

const fallbackJobs: Job[] = [
  {
    id: "job-f1-demo",
    tenantId: "pilot",
    state: "RUNNING",
    intent: { classification: "critical", priority: "latency" },
    prediction: { path: "f1-ultra-low-latency", strategy: "edge-quic-merkle", latencyMs: 350, failureGuard: "dual-path" },
    policyDecisions: [{ rule: "classification", action: "allow", reason: "tenant pilot has critical clearance" }],
  },
];

const fallbackKpi: KPI = {
  successRate: 0.99,
  p95LatencyMs: 480,
  sub500Ratio: 0.74,
  failures: 0,
  canceled: 0,
};

export default function App() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [kpi, setKpi] = useState<KPI | null>(null);
  const [filter, setFilter] = useState("ALL");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedJob, setSelectedJob] = useState<Job | null>(null);

  const fetchData = useCallback(async () => {
    try {
      setLoading(true);
      const [jobsPayload, kpiPayload] = await Promise.all([api<Job[]>("/jobs"), api<KPI>("/reports/kpi")]);
      setJobs(jobsPayload);
      setKpi(kpiPayload);
      setSelectedJob((current) => {
        if (!current) return null;
        return jobsPayload.find((j) => j.id === current.id) ?? null;
      });
      setError(null);
    } catch (err) {
      console.warn("Falling back to demo data", err);
      setJobs(fallbackJobs);
      setKpi(fallbackKpi);
      setError("Unable to reach orchestrator API. Showing demo data.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  const filteredJobs = useMemo(() => {
    if (filter === "ALL") return jobs;
    return jobs.filter((job) => job.state === filter);
  }, [jobs, filter]);

  const startScenario = async (scenario: Scenario) => {
    const payload =
      scenario === "f1"
        ? {
            intent: { classification: "critical", priority: "latency", pqc: true },
            files: [{ name: "f1-demo.bin", sizeBytes: 200_000_000, mime: "application/octet-stream", critical: true }],
            metadata: { scenario: "f1-demo" },
          }
        : {
            intent: { classification: "medical", priority: "reliability", pqc: true },
            files: [{ name: "clinic-demo.dcm", sizeBytes: 9_000_000_000, mime: "application/dicom", critical: false }],
            metadata: { scenario: "clinic-demo" },
          };
    try {
      await api("/jobs", { method: "POST", body: JSON.stringify(payload) });
      await fetchData();
    } catch (err) {
      setError(`Failed to start scenario: ${(err as Error).message}`);
    }
  };

  return (
    <div className="app-shell">
      <HeaderBar onRefresh={fetchData} loading={loading} onLaunch={startScenario} />
      {error && <StatusBanner message={error} tone="warning" />}

      <section className="panel kpi-grid">
        <MetricsCard
          title="Success Rate"
          value={kpi ? `${(kpi.successRate * 100).toFixed(1)}%` : "-"}
          subtitle="Target >= 99%"
          loading={loading}
        />
        <MetricsCard title="P95 Latency" value={kpi ? `${kpi.p95LatencyMs} ms` : "-"} subtitle="< 500 ms critical" loading={loading} />
        <MetricsCard
          title="<500 ms Ratio"
          value={kpi ? `${(kpi.sub500Ratio * 100).toFixed(0)}%` : "-"}
          subtitle="Critical path coverage"
          loading={loading}
        />
        <MetricsCard title="Failures" value={kpi ? kpi.failures.toString() : "-"} subtitle="24h rolling" loading={loading} />
      </section>

      <section className="jobs-surface">
        <div className="panel jobs-list">
          <div className="jobs-header">
            <div>
              <p className="eyebrow">Transfers</p>
              <h2>Jobs</h2>
            </div>
            <label className="filter">
              Filter
              <select value={filter} onChange={(e) => setFilter(e.target.value)}>
                <option value="ALL">All</option>
                <option value="QUEUED">Queued</option>
                <option value="RUNNING">Running</option>
                <option value="COMPLETED">Completed</option>
                <option value="FAILED">Failed</option>
                <option value="CANCELED">Canceled</option>
              </select>
            </label>
          </div>
          {loading ? (
            <div className="skeleton-table" aria-label="Loading jobs" />
          ) : (
            <table className="jobs-table">
              <thead>
                <tr>
                  <th>Job ID</th>
                  <th>State</th>
                  <th>Path</th>
                  <th>Latency</th>
                </tr>
              </thead>
              <tbody>
                {filteredJobs.map((job) => (
                  <tr
                    key={job.id}
                    onClick={() => setSelectedJob(job)}
                    className={selectedJob?.id === job.id ? "selected" : undefined}
                  >
                    <td className="mono">{job.id}</td>
                    <td className={`state-pill ${job.state.toLowerCase()}`}>{job.state}</td>
                    <td>{job.prediction?.path || "-"}</td>
                    <td>{job.prediction ? `${job.prediction.latencyMs} ms` : "-"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <div className="panel job-details">
          <div className="jobs-header">
            <div>
              <p className="eyebrow">Insights</p>
              <h3>Details</h3>
            </div>
          </div>
          {selectedJob ? (
            <>
              <dl className="details-grid">
                <div>
                  <dt>Tenant</dt>
                  <dd>{selectedJob.tenantId}</dd>
                </div>
                <div>
                  <dt>State</dt>
                  <dd className={`state-pill ${selectedJob.state.toLowerCase()}`}>{selectedJob.state}</dd>
                </div>
                <div>
                  <dt>Prediction</dt>
                  <dd>
                    {selectedJob.prediction ? (
                      <>
                        <strong>{selectedJob.prediction.path}</strong> - {selectedJob.prediction.strategy} - {selectedJob.prediction.latencyMs} ms
                      </>
                    ) : (
                      "-"
                    )}
                  </dd>
                </div>
                <div>
                  <dt>Receipt</dt>
                  <dd>{selectedJob.receipt?.receiptId || "Pending"}</dd>
                </div>
              </dl>

              <section className="detail-block">
                <h4>Intent</h4>
                <pre>{JSON.stringify(selectedJob.intent, null, 2)}</pre>
              </section>
              <section className="detail-block">
                <h4>Policy Decisions</h4>
                {selectedJob.policyDecisions?.length ? (
                  <ul className="bullet-list">
                    {selectedJob.policyDecisions.map((decision) => (
                      <li key={decision.rule}>
                        <strong>{decision.rule}</strong>: {decision.action} - {decision.reason}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="muted">No decisions recorded.</p>
                )}
              </section>
              <section className="detail-block">
                <h4>Decision Log</h4>
                {selectedJob.decisionLog?.length ? (
                  <ol className="numbered-list">
                    {selectedJob.decisionLog.map((entry, idx) => (
                      <li key={`${selectedJob.id}-log-${idx}`}>{entry}</li>
                    ))}
                  </ol>
                ) : (
                  <p className="muted">No log entries.</p>
                )}
              </section>
            </>
          ) : (
            <p className="muted">Select a job to inspect policy + receipts.</p>
          )}
        </div>
      </section>
    </div>
  );
}

type HeaderProps = {
  onRefresh: () => void;
  onLaunch: (scenario: Scenario) => void;
  loading: boolean;
};

function HeaderBar({ onRefresh, onLaunch, loading }: HeaderProps) {
  return (
    <header className="topbar">
      <div>
        <p className="eyebrow">Trackshift Admin</p>
        <h1>PQC transfers • Explainable decisions • Multi-path resiliency</h1>
      </div>
      <div className="actions">
        <button className="btn btn-outline" onClick={() => onLaunch("f1")}>
          Launch F1 Job
        </button>
        <button className="btn btn-outline" onClick={() => onLaunch("clinic")}>
          Launch Clinic Job
        </button>
        <button className="btn btn-primary" onClick={onRefresh} disabled={loading}>
          Refresh
        </button>
      </div>
    </header>
  );
}

type StatusBannerProps = { message: string; tone?: "warning" | "info" };
function StatusBanner({ message, tone = "info" }: StatusBannerProps) {
  return <div className={`status-banner ${tone}`}>{message}</div>;
}

type MetricsCardProps = { title: string; value: string; subtitle: string; loading?: boolean };

function MetricsCard({ title, value, subtitle, loading }: MetricsCardProps) {
  return (
    <article className="metric-card">
      <p className="metric-title">{title}</p>
      {loading ? <div className="skeleton-value" /> : <p className="metric-value">{value}</p>}
      <p className="metric-subtitle">{subtitle}</p>
    </article>
  );
}
