const BASE = import.meta.env.VITE_API_BASE || '';

function getToken(): string | null {
  return localStorage.getItem('wsvpn_token');
}

export function setToken(token: string) {
  localStorage.setItem('wsvpn_token', token);
}

export function clearToken() {
  localStorage.removeItem('wsvpn_token');
}

async function request<T>(path: string, opts: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(opts.headers as Record<string, string> || {}),
  };
  const token = getToken();
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }
  const res = await fetch(BASE + path, { ...opts, headers });
  if (res.status === 401) {
    clearToken();
    throw new Error('unauthorized');
  }
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`);
  return body as T;
}

// ── Auth ──

export interface LoginResponse { token: string }

export async function login(username: string, password: string): Promise<LoginResponse> {
  return request<LoginResponse>('/api/web/login', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  });
}

// ── Devices ──

export interface Device {
  id: number;
  device_id: string;
  device_info: string;
  name: string;
  virtual_ip: string | null;
  auto_vip: string;
  status: 'pending' | 'approved' | 'revoked';
  key_expires_at: string | null;
  last_seen_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface DeviceListResponse { devices: Device[] }

export async function listDevices(): Promise<DeviceListResponse> {
  return request<DeviceListResponse>('/api/web/devices');
}

export async function getDevice(deviceID: string): Promise<Device> {
  return request<Device>(`/api/web/devices/${deviceID}`);
}

export async function updateDevice(deviceID: string, data: Partial<Device>): Promise<Device> {
  return request<Device>(`/api/web/devices/${deviceID}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  });
}

export async function approveDevice(deviceID: string): Promise<Device> {
  return request<Device>(`/api/web/devices/${deviceID}/approve`, { method: 'POST' });
}

export async function revokeDevice(deviceID: string): Promise<Device> {
  return request<Device>(`/api/web/devices/${deviceID}/revoke`, { method: 'POST' });
}

export async function deleteDevice(deviceID: string): Promise<void> {
  await request(`/api/web/devices/${deviceID}`, { method: 'DELETE' });
}

// ── Device Auth (public) ──

export interface AuthInitResponse {
  session_code: string;
  auth_url: string;
}

export interface AuthSessionInfo {
  session: { session_code: string; device_id: string; status: string; expires_at: string };
  device: Device;
}

export async function getAuthSession(code: string): Promise<AuthSessionInfo> {
  return request<AuthSessionInfo>(`/api/auth/session/${code}`);
}

export async function approveAuthSession(code: string): Promise<{ status: string }> {
  return request(`/api/auth/session/${code}`, { method: 'POST' });
}
