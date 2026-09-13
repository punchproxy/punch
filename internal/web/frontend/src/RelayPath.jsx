import { shortName } from "./utils.js";

export default function RelayPath({ path = [], label = "Path" }) {
  if (!path.length) return null;
  return <div className="relay-path" aria-label={label}>
    <span className="relay-path-label muted">{label}</span>
    <ol aria-label="Traffic order">
      {path.map((hop, index) => <li key={`${hop.group}-${hop.relay}-${index}`}>
        <span className="relay-path-hop" title={`${hop.group} / ${shortName(hop.relay, hop.group)}`}>
          <span className="faint">{hop.group}</span>
          <strong className="mono">{shortName(hop.relay, hop.group)}</strong>
          <span className="relay-path-role">{index === path.length - 1 ? "exit" : "transit"}</span>
        </span>
      </li>)}
    </ol>
  </div>;
}
