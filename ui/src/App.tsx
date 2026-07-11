import { useCallback, useEffect, useState } from "react";
import { NavLink, Route, Routes, useNavigate } from "react-router-dom";
import { api, toggleTheme } from "./api";
import { Icon } from "./icons";
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

type NavItem = { to: string; label: string; icon: keyof typeof Icon; end?: boolean };
const nav: { section: string; items: NavItem[] }[] = [
  { section: "Manage", items: [
    { to: "/", label: "Dashboard", icon: "dashboard", end: true },
    { to: "/sites", label: "Sites", icon: "sites" },
    { to: "/logs", label: "Logs", icon: "logs" },
  ]},
  { section: "Server", items: [
    { to: "/services", label: "Services", icon: "services" },
    { to: "/firewall", label: "Firewall", icon: "firewall" },
    { to: "/users", label: "Users", icon: "users" },
  ]},
  { section: "Account", items: [
    { to: "/security", label: "Security", icon: "shield" },
    { to: "/settings", label: "Settings", icon: "settings" },
    { to: "/audit", label: "Audit log", icon: "audit" },
  ]},
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
        <div className="logo"><span className="dot" /> Slipstream</div>
        {nav.map((group) => (
          <div key={group.section}>
            <div className="nav-section">{group.section}</div>
            {group.items.map((item) => {
              const IconCmp = Icon[item.icon];
              return (
                <NavLink key={item.to} to={item.to} end={item.end}
                  className={({ isActive }) => "nav-link" + (isActive ? " active" : "")}>
                  <span className="ico"><IconCmp /></span> {item.label}
                </NavLink>
              );
            })}
          </div>
        ))}
        <div className="spacer" />
        <div className="sidebar-foot">
          <div className="sidebar-user">
            <span className="avatar">{me.email[0].toUpperCase()}</span>
            <div style={{ minWidth: 0, flex: 1 }}>
              <div style={{ fontSize: 12.5, fontWeight: 550, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{me.email}</div>
              <div className="dim3" style={{ fontSize: 11 }}>{me.role}{me.totp_enabled ? " · 2FA on" : ""}</div>
            </div>
            <button className="icon-btn" title="Toggle theme" onClick={toggleTheme}><Icon.theme /></button>
            <button className="icon-btn" title="Sign out" onClick={logout}><Icon.logout /></button>
          </div>
        </div>
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
