import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'

import { AuditLog } from '../screens/AuditLog'
import { Board } from '../screens/Board'
import { Devices } from '../screens/Devices'
import { InstanceAdmin } from '../screens/InstanceAdmin'
import { Invites } from '../screens/Invites'
import { Join } from '../screens/Join'
import { Members } from '../screens/Members'
import { Setup } from '../screens/Setup'
import { SignIn } from '../screens/SignIn'
import { TargetDetail } from '../screens/TargetDetail'
import { TimerOverrides } from '../screens/TimerOverrides'
import { Shell } from './Shell'
import { PrincipalProvider } from './principal'

export function App() {
  return (
    <BrowserRouter>
      <PrincipalProvider>
        <Routes>
          {/* Public. `/join` is the landing an invite link points at, and it is also where the
              OAuth callback returns with a ticket in the fragment — one route for both, because
              both arrive the same way. */}
          <Route path="/join" element={<Join />} />
          <Route path="/signin" element={<SignIn />} />

          {/* Public, and authorised by `TOD_SETUP_TOKEN` rather than by a session: on the database
              it exists for, no credential has ever been issued. It sits OUTSIDE the Shell for the
              same reason `/join` does — there is no principal to draw a nav from. ADR-0016. */}
          <Route path="/setup" element={<Setup />} />

          <Route element={<Shell />}>
            <Route path="/" element={<Navigate to="/board" replace />} />
            <Route path="/board" element={<Board />} />
            <Route path="/board/:targetId" element={<TargetDetail />} />
            <Route path="/members" element={<Members />} />
            <Route path="/invites" element={<Invites />} />
            <Route path="/timers" element={<TimerOverrides />} />
            <Route path="/audit" element={<AuditLog />} />
            <Route path="/devices" element={<Devices />} />
            <Route path="/admin/providers" element={<InstanceAdmin />} />
          </Route>

          <Route path="*" element={<Navigate to="/board" replace />} />
        </Routes>
      </PrincipalProvider>
    </BrowserRouter>
  )
}
