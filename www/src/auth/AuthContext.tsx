import { useState, useCallback, type ReactNode } from 'react'
import { login as apiLogin, setToken, clearToken } from '../api/client'
import { AuthContext } from './authContextValue'

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setTokenState] = useState<string | null>(localStorage.getItem('wsvpn_token'))

  const login = useCallback(async (u: string, p: string) => {
    const res = await apiLogin(u, p)
    setToken(res.token)
    setTokenState(res.token)
  }, [])

  const logout = useCallback(() => {
    clearToken()
    setTokenState(null)
  }, [])

  return (
    <AuthContext.Provider value={{ token, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}
