package api

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/health"
)

// ts converts a time, leaving zero times unset.
func ts(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// fromTS converts back; unset is the zero time.
func fromTS(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func counts(in []health.NamedCount) []*pb.NamedCount {
	out := make([]*pb.NamedCount, 0, len(in))
	for _, c := range in {
		out = append(out, &pb.NamedCount{Name: c.Name, Count: c.Count})
	}
	return out
}

func fromCounts(in []*pb.NamedCount) []health.NamedCount {
	var out []health.NamedCount
	for _, c := range in {
		out = append(out, health.NamedCount{Name: c.GetName(), Count: c.GetCount()})
	}
	return out
}

func healthToProto(r *health.Report) *pb.HealthResponse {
	d, a, s, in := r.Daemon, r.API, r.Storage, r.Integrity
	resp := &pb.HealthResponse{
		CheckedAt:     ts(r.CheckedAt),
		HistoryBegins: ts(r.HistoryBegins),
		Daemon: &pb.HealthResponse_Daemon{
			Version: d.Version, Started: ts(d.Started), CpuPercent: d.CPUPercent, CpuWindow: durationpb.New(d.CPUWindow),
			RssBytes: d.RSSBytes, Socket: d.Socket, User: d.User, Root: d.Root,
		},
		Api: &pb.HealthResponse_API{
			Since: ts(a.Since), Requests: a.Requests, PerSecNow: a.PerSecNow,
			P50: durationpb.New(a.P50), P95: durationpb.New(a.P95), P99: durationpb.New(a.P99),
			LatencyCount: int64(a.LatencyCount), Errors: a.Errors,
			ErrorsByCode: counts(a.ErrorsByCode), Methods: counts(a.Methods), Clients: a.Clients,
		},
		Storage: &pb.HealthResponse_Storage{
			DataDir: s.DataDir, DbPath: s.DBPath, DbBytes: s.DBBytes, FreeBytes: s.FreeBytes, SizeBytes: s.SizeBytes,
		},
		Integrity: &pb.HealthResponse_Integrity{
			CheckedAt: ts(in.CheckedAt), FullAt: ts(in.FullAt), Records: in.Records, Ok: in.OK,
			BadRecordId: in.BadRecordID, Interval: durationpb.New(in.Interval), GapTracking: in.GapTracking,
		},
		Sudo: &pb.HealthResponse_Sudo{Checked: r.Sudo.Checked, Reason: r.Sudo.Reason, Passwordless: r.Sudo.Passwordless},
	}
	for _, g := range r.Ingest {
		resp.Ingest = append(resp.Ingest, &pb.HealthResponse_IngestAgent{
			Agent: g.Agent, LastRecord: ts(g.LastRecord), Files: g.Files, Unknown: g.Unknown,
		})
	}
	for _, g := range r.Agents {
		resp.Agents = append(resp.Agents, &pb.HealthResponse_Agent{Agent: g.Agent, Version: g.Version, Logs: g.Logs})
	}
	for _, l := range r.Live {
		resp.Live = append(resp.Live, &pb.HealthResponse_LiveSession{
			SessionId: l.SessionID, Agent: l.Agent, Repo: l.Repo, LastActivity: ts(l.LastActivity),
		})
	}
	return resp
}

func healthFromProto(p *pb.HealthResponse) *health.Report {
	d, a, s, in := p.GetDaemon(), p.GetApi(), p.GetStorage(), p.GetIntegrity()
	r := &health.Report{
		CheckedAt:     fromTS(p.GetCheckedAt()),
		HistoryBegins: fromTS(p.GetHistoryBegins()),
		Daemon: health.Daemon{
			Version: d.GetVersion(), Started: fromTS(d.GetStarted()), CPUPercent: d.GetCpuPercent(),
			CPUWindow: d.GetCpuWindow().AsDuration(), RSSBytes: d.GetRssBytes(), Socket: d.GetSocket(),
			User: d.GetUser(), Root: d.GetRoot(),
		},
		API: health.API{
			Since: fromTS(a.GetSince()), Requests: a.GetRequests(), PerSecNow: a.GetPerSecNow(),
			P50: a.GetP50().AsDuration(), P95: a.GetP95().AsDuration(), P99: a.GetP99().AsDuration(),
			LatencyCount: int(a.GetLatencyCount()), Errors: a.GetErrors(),
			ErrorsByCode: fromCounts(a.GetErrorsByCode()), Methods: fromCounts(a.GetMethods()), Clients: a.GetClients(),
		},
		Storage: health.Storage{
			DataDir: s.GetDataDir(), DBPath: s.GetDbPath(), DBBytes: s.GetDbBytes(),
			FreeBytes: s.GetFreeBytes(), SizeBytes: s.GetSizeBytes(),
		},
		Integrity: health.Integrity{
			CheckedAt: fromTS(in.GetCheckedAt()), FullAt: fromTS(in.GetFullAt()), Records: in.GetRecords(), OK: in.GetOk(),
			BadRecordID: in.GetBadRecordId(), Interval: in.GetInterval().AsDuration(), GapTracking: in.GetGapTracking(),
		},
		Sudo: health.Sudo{Checked: p.GetSudo().GetChecked(), Reason: p.GetSudo().GetReason(), Passwordless: p.GetSudo().GetPasswordless()},
	}
	for _, g := range p.GetIngest() {
		r.Ingest = append(r.Ingest, health.IngestAgent{
			Agent: g.GetAgent(), LastRecord: fromTS(g.GetLastRecord()), Files: g.GetFiles(), Unknown: g.GetUnknown(),
		})
	}
	for _, g := range p.GetAgents() {
		r.Agents = append(r.Agents, health.Agent{Agent: g.GetAgent(), Version: g.GetVersion(), Logs: g.GetLogs()})
	}
	for _, l := range p.GetLive() {
		r.Live = append(r.Live, health.LiveSession{
			SessionID: l.GetSessionId(), Agent: l.GetAgent(), Repo: l.GetRepo(), LastActivity: fromTS(l.GetLastActivity()),
		})
	}
	return r
}
