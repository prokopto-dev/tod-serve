import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'

import { AuditLog } from '../screens/AuditLog'
import { Board } from '../screens/Board'
import { CircleSettings } from '../screens/CircleSettings'
import { Devices } from '../screens/Devices'
import { InstanceAdmin } from '../screens/InstanceAdmin'
import { Invites } from '../screens/Invites'
import { Join } from '../screens/Join'
import { Landing } from '../screens/Landing'
import { Members } from '../screens/Members'
import { NewCircle } from '../screens/NewCircle'
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
          {/* Public, and OUTSIDE the Shell: `/` is written for somebody who has never been here,
              so it draws no nav and reads no principal beyond deciding whether to redirect. A
              visitor who IS signed in goes to the board, which is what they came for. */}
          <Route path="/" element={<Landing />} />

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
            <Route path="/board" element={<Board />} />
            <Route path="/board/:targetId" element={<TargetDetail />} />
            <Route path="/members" element={<Members />} />
            <Route path="/invites" element={<Invites />} />
            <Route path="/timers" element={<TimerOverrides />} />
            <Route path="/audit" element={<AuditLog />} />
            <Route path="/devices" element={<Devices />} />
            <Route path="/settings" element={<CircleSettings />} />
            {/* Instance-realm, so it is NOT under /settings: an instance owner who is an ordinary
                member here sees no settings section at all. See screens/NewCircle.tsx. */}
            <Route path="/circles/new" element={<NewCircle />} />
            <Route path="/admin/providers" element={<InstanceAdmin />} />
          </Route>

          {/* An unknown path lands on `/`, which decides: the board for a signed-in visitor,
              the landing page for anybody else. Sending it straight to `/board` bounced a
              signed-out visitor to a sign-in form with no statement of what they were signing
              into. */}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </PrincipalProvider>
    </BrowserRouter>
  )
}
