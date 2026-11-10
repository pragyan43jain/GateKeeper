// Adapted from the user-supplied 21st.dev SaaS Template by waleedkibhen.
import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { cn } from '../../lib/utils'
interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'default' | 'secondary' | 'ghost'
}
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'default', className, type = 'button', ...props }, ref,
) {
  return <button ref={ref} type={type} className={cn('console-button', `console-button-${variant}`, className)} {...props} />
})
