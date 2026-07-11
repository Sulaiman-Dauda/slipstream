import { useCallback, useEffect, useState } from "react";
import { NavLink, Route, Routes, useNavigate } from "react-router-dom";
import { api } from "./api";
import Auth from "./pages/Auth";
import Dashboard from "./pages/Dashboard";
import Sites from "./pages/Sites";
import SiteDetail from "./pages/SiteDetail";
import Settings from "./pages/Settings";
import Audit from "./pages/Audit";

interface Me {
  email: string;
}

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
          slip<span>stream</span>
        </div>
        <NavLink to="/" end className={({ isActive }) => "nav-link" + (isActive ? " active" : "")}>
          Dashboard
        </NavLink>
        <NavLink to="/sites" className={({ isActive }) => "nav-link" + (isActive ? " active" : "")}>
          Sites
        </NavLink>
        <NavLink to="/audit" className={({ isActive }) => "nav-link" + (isActive ? " active" : "")}>
          Audit log
        </NavLink>
        <NavLink to="/settings" className={({ isActive }) => "nav-link" + (isActive ? " active" : "")}>
          Settings
        </NavLink>
        <div className="spacer" />
        <div className="dim" style={{ padding: "0 10px", fontSize: 12 }}>
          {me.email}
        </div>
        <button className="ghost small" style={{ margin: "8px 10px" }} onClick={logout}>
          Sign out
        </button>
      </nav>
      <main className="main">
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/sites" element={<Sites />} />
          <Route path="/sites/:id" element={<SiteDetail />} />
          <Route path="/audit" element={<Audit />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Dashboard />} />
        </Routes>
      </main>
    </div>
  );
}
