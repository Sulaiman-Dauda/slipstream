package state

// RecordMetric stores a capacity sample and enforces a fixed 288-row
// retention window (24 hours at the scheduler's five-minute cadence).
func (s *Store) RecordMetric(m MetricSample) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO metric_samples
		(cpu_headroom_pct, mem_headroom_pct, disk_headroom_pct, load1, sampled_at)
		VALUES (?, ?, ?, ?, ?)`, m.CPUHeadroomPct, m.MemHeadroomPct,
		m.DiskHeadroomPct, m.Load1, now()); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM metric_samples WHERE id NOT IN
		(SELECT id FROM metric_samples ORDER BY id DESC LIMIT 288)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListMetrics(limit int) ([]MetricSample, error) {
	if limit <= 0 || limit > 288 {
		limit = 288
	}
	rows, err := s.db.Query(`SELECT cpu_headroom_pct, mem_headroom_pct,
		disk_headroom_pct, load1, sampled_at FROM metric_samples
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reversed []MetricSample
	for rows.Next() {
		var m MetricSample
		var sampled string
		if err := rows.Scan(&m.CPUHeadroomPct, &m.MemHeadroomPct, &m.DiskHeadroomPct, &m.Load1, &sampled); err != nil {
			return nil, err
		}
		m.SampledAt = parseTime(sampled)
		reversed = append(reversed, m)
	}
	result := make([]MetricSample, len(reversed))
	for i := range reversed {
		result[len(reversed)-1-i] = reversed[i]
	}
	return result, rows.Err()
}
