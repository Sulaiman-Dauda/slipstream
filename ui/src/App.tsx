import { useCallback, useEffect, useState } from "react";
import { NavLink, Route, Routes, useNavigate } from "react-router-dom";
import { api, toggleTheme } from "./api";
import Auth from "./pages/Auth";
import Dashboard from "./pages/Dashboard";
import Sites from "./pages/Sites";
import SiteDetail from "./pages/SiteDetail";
import Settings from "./pages/Settings";
import Audit from "./pages/Audit";
import Security from "./pages/Security";
import Users from "./pages/Users";
import Services from "./pages/Services";
import Firewall from "./pages/Firewall";
import Logs from "./pages/Logs";

interface Me {
  email: string;
  role: string;
  totp_enabled: boolean;
}

const nav = [
  { section: "Manage" },
  { to: "/", label: "Dashboard", ico: "▦", end: true },
  { to: "/sites", label: "Sites", ico: "◫" },
  { to: "/logs", label: "Logs", ico: "☰" },
  { section: "Server" },
  { to: "/services", label: "Services", ico: "⚙" },
  { to: "/firewall", label: "Firewall", ico: "▩" },
  { to: "/users", label: "Users", ico: "◍" },
  { section: "Account" },
  { to: "/security", label: "Security", ico: "⛨" },
  { to: "/settings", label: "Settings", ico: "⚑" },
  { to: "/audit", label: "Audit log", ico: "◷" },
];

export default function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [checked, setChecked] = useState(false);
  const navigate = useNavigate();

  const refresh = useCallback(async () => {
    try {
      setMe(await api.get<Me>("/api/me"));
    } catch {
      setMe(null);
    } finally {
      setChecked(true);
    }
  }, []);

  useEffect(() => {
    refresh();
    const onUnauthorized = () => setMe(null);
    window.addEventListener("slipstream:unauthorized", onUnauthorized);
    return () => window.removeEventListener("slipstream:unauthorized", onUnauthorized);
  }, [refresh]);

  if (!checked) return null;
  if (!me) return <Auth onAuthed={refresh} />;

  const logout = async () => {
    await api.post("/api/logout");
    setMe(null);
    navigate("/");
  };

  return (
    <div className="layout">
      <nav className="sidebar">
        <div className="logo">
          <span className="dot" /> slipstream
        </div>
        {nav.map((item, i) =>
          "section" in item ? (
            <div className="nav-section" key={i}>{item.section}</div>
          ) : (
            <NavLink
              key={item.to}
              to={item.to!}
              end={item.end}
              className={({ isActive }) => "nav-link" + (isActive ? " active" : "")}
            >
              <span className="ico">{item.ico}</span> {item.label}
            </NavLink>
          ),
        )}
        <div className="spacer" />
        <button className="ghost small" onClick={toggleTheme} style={{ margin: "4px 6px" }}>
          ◐ Theme
        </button>
        <div className="dim3" style={{ padding: "6px 10px 2px", fontSize: 12 }}>{me.email}</div>
        <button className="ghost small" style={{ margin: "2px 6px" }} onClick={logout}>Sign out</button>
      </nav>
      <main className="main">
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/sites" element={<Sites />} />
          <Route path="/sites/:id" element={<SiteDetail />} />
          <Route path="/services" element={<Services />} />
          <Route path="/firewall" element={<Firewall />} />
          <Route path="/logs" element={<Logs />} />
          <Route path="/users" element={<Users />} />
          <Route path="/security" element={<Security me={me} onChange={refresh} />} />
          <Route path="/audit" element={<Audit />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Dashboard />} />
        </Routes>
      </main>
    </div>
  );
}
