import { Suspense, lazy, type ReactNode } from 'react'
import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { AuthProvider } from './auth/AuthContext'
import { useAuth } from './auth/useAuth'

const Login = lazy(() => import('./pages/Login'))
const Dashboard = lazy(() => import('./pages/Dashboard'))
const DeviceAuth = lazy(() => import('./pages/DeviceAuth'))
const DeviceDetail = lazy(() => import('./pages/DeviceDetail'))

function RequireAuth({ children }: { children: ReactNode }) {
  const { token } = useAuth()
  const location = useLocation()
  if (!token) return <Navigate to="/admin/login" replace state={{ from: location.pathname + location.search }} />
  return <>{children}</>
}

export default function App() {
  return (
    <AuthProvider>
      <Suspense fallback={null}>
        <Routes>
          <Route path="/" element={<Navigate to="/admin/" replace />} />
          <Route path="/admin/" element={<Navigate to="/admin/dashboard" replace />} />
          <Route path="/admin/login" element={<Login />} />
          <Route path="/admin/dashboard" element={<RequireAuth><Dashboard /></RequireAuth>} />
          <Route path="/admin/devices/:deviceID" element={<RequireAuth><DeviceDetail /></RequireAuth>} />
          <Route path="/admin/device-auth" element={<RequireAuth><DeviceAuth /></RequireAuth>} />
        </Routes>
      </Suspense>
    </AuthProvider>
  )
}
