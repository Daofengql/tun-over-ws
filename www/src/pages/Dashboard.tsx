import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import Container from '@mui/material/Container'
import Box from '@mui/material/Box'
import Typography from '@mui/material/Typography'
import Button from '@mui/material/Button'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableContainer from '@mui/material/TableContainer'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Paper from '@mui/material/Paper'
import Chip from '@mui/material/Chip'
import IconButton from '@mui/material/IconButton'
import Dialog from '@mui/material/Dialog'
import DialogTitle from '@mui/material/DialogTitle'
import DialogContent from '@mui/material/DialogContent'
import DialogActions from '@mui/material/DialogActions'
import TextField from '@mui/material/TextField'
import Alert from '@mui/material/Alert'
import Tooltip from '@mui/material/Tooltip'
import EditIcon from '@mui/icons-material/Edit'
import DeleteIcon from '@mui/icons-material/Delete'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import BlockIcon from '@mui/icons-material/Block'
import VisibilityIcon from '@mui/icons-material/Visibility'
import CircularProgress from '@mui/material/CircularProgress'
import { useAuth } from '../auth/useAuth'
import { approveDevice, listDevices, revokeDevice, updateDevice, deleteDevice, type Device } from '../api/client'
import type { ChipProps } from '@mui/material/Chip'

const statusColor = (s: string): ChipProps['color'] => {
  if (s === 'approved') return 'success'
  if (s === 'pending') return 'warning'
  return 'error'
}

function errorMessage(err: unknown) {
  return err instanceof Error ? err.message : '请求失败'
}

const statusLabel = (s: string) => {
  if (s === 'approved') return '已授权'
  if (s === 'pending') return '待授权'
  return '已吊销'
}

export default function Dashboard() {
  const [devices, setDevices] = useState<Device[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editDevice, setEditDevice] = useState<Device | null>(null)
  const [editName, setEditName] = useState('')
  const [editVIP, setEditVIP] = useState('')
  const { logout } = useAuth()
  const navigate = useNavigate()

  const load = async () => {
    try {
      setLoading(true)
      const res = await listDevices()
      setDevices(res.devices || [])
      setError('')
    } catch (err: unknown) {
      const message = errorMessage(err)
      if (message === 'unauthorized') { navigate('/admin/login'); return }
      setError(message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    let cancelled = false
    async function loadInitial() {
      try {
        setLoading(true)
        const res = await listDevices()
        if (cancelled) return
        setDevices(res.devices || [])
        setError('')
      } catch (err: unknown) {
        if (cancelled) return
        const message = errorMessage(err)
        if (message === 'unauthorized') { navigate('/admin/login'); return }
        setError(message)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void loadInitial()
    return () => { cancelled = true }
  }, [navigate])

  const handleApprove = async (deviceID: string) => {
    await approveDevice(deviceID)
    load()
  }

  const handleRevoke = async (deviceID: string) => {
    await revokeDevice(deviceID)
    load()
  }

  const handleDelete = async (deviceID: string) => {
    if (!confirm('确定要删除此设备吗？')) return
    await deleteDevice(deviceID)
    load()
  }

  const openEdit = (d: Device) => {
    setEditDevice(d)
    setEditName(d.name)
    setEditVIP(d.virtual_ip || '')
  }

  const handleSave = async () => {
    if (!editDevice) return
    await updateDevice(editDevice.device_id, {
      name: editName,
      virtual_ip: editVIP || null,
    })
    setEditDevice(null)
    load()
  }

  const parseDeviceInfo = (info: string) => {
    try {
      const obj = JSON.parse(info)
      return `${obj.os}/${obj.arch}`
    } catch { return info }
  }

  return (
    <Container maxWidth="lg" sx={{ mt: 4 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Typography variant="h4">设备管理</Typography>
        <Box>
          <Button variant="outlined" onClick={load} sx={{ mr: 1 }}>刷新</Button>
          <Button variant="outlined" color="inherit" onClick={() => { logout(); navigate('/admin/login') }}>
            退出
          </Button>
        </Box>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}><CircularProgress /></Box>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>设备 ID</TableCell>
                <TableCell>名称</TableCell>
                <TableCell>设备信息</TableCell>
                <TableCell>虚拟 IP</TableCell>
                <TableCell>状态</TableCell>
                <TableCell>最后在线</TableCell>
                <TableCell align="right">操作</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {devices.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} align="center" sx={{ py: 6, color: 'text.secondary' }}>
                    暂无设备
                  </TableCell>
                </TableRow>
              ) : devices.map(d => (
                <TableRow key={d.device_id} hover>
                  <TableCell>
                    <Tooltip title={d.device_id}>
                      <Button size="small" onClick={() => navigate(`/admin/devices/${d.device_id}`)}>
                        {d.device_id.substring(0, 16)}...
                      </Button>
                    </Tooltip>
                  </TableCell>
                  <TableCell>{d.name || '-'}</TableCell>
                  <TableCell>{parseDeviceInfo(d.device_info)}</TableCell>
                  <TableCell>{d.virtual_ip || d.auto_vip}</TableCell>
                  <TableCell>
                    <Chip label={statusLabel(d.status)} color={statusColor(d.status)} size="small" />
                  </TableCell>
                  <TableCell>
                    {d.last_seen_at ? new Date(d.last_seen_at).toLocaleString() : '从未'}
                  </TableCell>
                  <TableCell align="right">
                    {d.status === 'pending' && (
                      <Tooltip title="授权">
                        <IconButton color="success" onClick={() => handleApprove(d.device_id)}>
                          <CheckCircleIcon />
                        </IconButton>
                      </Tooltip>
                    )}
                    {d.status === 'approved' && (
                      <Tooltip title="吊销">
                        <IconButton color="warning" onClick={() => handleRevoke(d.device_id)}>
                          <BlockIcon />
                        </IconButton>
                      </Tooltip>
                    )}
                    <Tooltip title="编辑">
                      <IconButton onClick={() => openEdit(d)}><EditIcon /></IconButton>
                    </Tooltip>
                    <Tooltip title="详情">
                      <IconButton onClick={() => navigate(`/admin/devices/${d.device_id}`)}><VisibilityIcon /></IconButton>
                    </Tooltip>
                    <Tooltip title="删除">
                      <IconButton color="error" onClick={() => handleDelete(d.device_id)}>
                        <DeleteIcon />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Edit dialog */}
      <Dialog open={!!editDevice} onClose={() => setEditDevice(null)} maxWidth="sm" fullWidth>
        <DialogTitle>编辑设备</DialogTitle>
        <DialogContent>
          <TextField
            label="设备名称"
            value={editName}
            onChange={e => setEditName(e.target.value)}
            fullWidth sx={{ mt: 1, mb: 2 }}
          />
          <TextField
            label="自定义虚拟 IP"
            value={editVIP}
            onChange={e => setEditVIP(e.target.value)}
            placeholder="留空使用自动分配"
            helperText="格式: 10.66.0.x，留空则使用自动分配的 IP"
            fullWidth
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setEditDevice(null)}>取消</Button>
          <Button variant="contained" onClick={handleSave}>保存</Button>
        </DialogActions>
      </Dialog>
    </Container>
  )
}
