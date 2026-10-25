import { validRuns } from '../data/results';

// Renders only real runs with full provenance. The section is hidden while the list is empty.
export default function RecordedResults() {
  return (
    <>
      {validRuns.map(run => (
        <article key={run.id} className="run reveal">
          <header className="run__head">
            <h3>{run.title}</h3>
            <span className="run__kind">{run.kind === 'measured' ? 'Measured run' : 'Replay of a recorded run'}</span>
          </header>
          <p>{run.summary}</p>
          <dl className="run__meta">
            <div><dt>Recorded</dt><dd>{run.recordedAt}</dd></div>
            <div><dt>Commit</dt><dd>{run.sourceCommit}</dd></div>
            <div><dt>Environment</dt><dd>{run.environment}</dd></div>
            <div><dt>Workload</dt><dd>{run.workload}</dd></div>
          </dl>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>{run.columns.map(c => <th key={c} scope="col">{c}</th>)}</tr>
              </thead>
              <tbody>
                {run.rows.map((row, i) => (
                  <tr key={i}>{row.map((cell, j) => (j === 0 ? <th key={j} scope="row">{cell}</th> : <td key={j}>{cell}</td>))}</tr>
                ))}
              </tbody>
            </table>
          </div>
          {run.notes && run.notes.length > 0 && <ul className="run__notes">{run.notes.map(n => <li key={n}>{n}</li>)}</ul>}
          <a href={run.rawDataUrl}>Raw data</a>
        </article>
      ))}
    </>
  );
}
