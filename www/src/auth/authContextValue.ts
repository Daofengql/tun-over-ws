import { createContext } from 'react'

export interface AuthCtx {
  token: string | null;
  login: (u: string, p: string) => Promise<void>;
  logout: () => void;
}

export const AuthContext = createContext<AuthCtx>({ token: null, login: async () => {}, logout: () => {} })
